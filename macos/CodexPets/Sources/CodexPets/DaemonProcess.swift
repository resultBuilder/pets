import Foundation

/// Supervises the bundled Go `pi-pet-daemon` as a child process. The daemon
/// owns the Unix socket; the app talks to it through `DaemonClient` exactly
/// like every other client. The child holds a stdin pipe and exits on EOF
/// (`-watch-stdin`), so it cannot outlive the app even after a crash.
final class DaemonProcessController {
    private enum Constants {
        static let socketReadyTimeout: TimeInterval = 10
        static let socketPollInterval: TimeInterval = 0.05
        static let restartDelay: TimeInterval = 1
        static let maxStartsPerWindow = 5
        static let startWindow: TimeInterval = 30
        static let stopGracePeriod: TimeInterval = 2
    }

    private let binaryURL: URL
    private let socketPath: String
    private let logURL: URL?
    private let piExtensionSourceURL: URL?
    private let stateFileURL: URL?
    private let updateBuildScript: String?
    private let petRoots: [(source: String, url: URL)]
    private let petImportRootURL: URL?
    private let catalogDirURL: URL?
    private let dialogueHistoryURL: URL?
    private let queue = DispatchQueue(label: "CodexPets.DaemonProcess")
    private var process: Process?
    private var stdinPipe: Pipe?
    private var stopping = false
    private var startTimes: [Date] = []

    /// Called on the main queue every time the daemon socket becomes ready:
    /// once after start() and again after every automatic restart. Callers
    /// should (re)subscribe and republish daemon state from here.
    var onReady: (() -> Void)?
    /// Called on the main queue when the daemon cannot be (re)started.
    var onFailure: ((String) -> Void)?

    init(
        binaryURL: URL,
        socketPath: String,
        logURL: URL? = nil,
        piExtensionSourceURL: URL? = nil,
        stateFileURL: URL? = nil,
        updateBuildScript: String? = nil,
        petRoots: [(source: String, url: URL)] = [],
        petImportRootURL: URL? = nil,
        catalogDirURL: URL? = nil,
        dialogueHistoryURL: URL? = nil
    ) {
        self.binaryURL = binaryURL
        self.socketPath = socketPath
        self.logURL = logURL
        self.piExtensionSourceURL = piExtensionSourceURL
        self.stateFileURL = stateFileURL
        self.updateBuildScript = updateBuildScript
        self.petRoots = petRoots
        self.petImportRootURL = petImportRootURL
        self.catalogDirURL = catalogDirURL
        self.dialogueHistoryURL = dialogueHistoryURL
    }

    static func bundledPiExtensionSourceURL() -> URL? {
        var candidates: [URL] = []
        if let resourceURL = Bundle.main.resourceURL {
            candidates.append(
                resourceURL
                    .appendingPathComponent("PiExtension", isDirectory: true)
                    .appendingPathComponent("index.ts")
            )
        }
        candidates.append(
            URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
                .appendingPathComponent("pi-extension", isDirectory: true)
                .appendingPathComponent("index.ts")
        )
        return candidates.first { FileManager.default.fileExists(atPath: $0.path) }
    }

    static func bundledDaemonURL() -> URL? {
        if let url = Bundle.main.url(forAuxiliaryExecutable: "pi-pet-daemon"),
           FileManager.default.isExecutableFile(atPath: url.path)
        {
            return url
        }
        if let executable = Bundle.main.executableURL {
            let sibling = executable.deletingLastPathComponent().appendingPathComponent("pi-pet-daemon")
            if FileManager.default.isExecutableFile(atPath: sibling.path) {
                return sibling
            }
        }
        return nil
    }

    func start() {
        queue.async { [weak self] in
            self?.startOnQueue()
        }
    }

    func stop() {
        queue.sync {
            stopOnQueue()
        }
    }

    private func startOnQueue() {
        guard !stopping, process == nil else { return }

        let now = Date()
        startTimes = startTimes.filter { now.timeIntervalSince($0) < Constants.startWindow }
        guard startTimes.count < Constants.maxStartsPerWindow else {
            reportFailure("pi-pet-daemon restarted too often; giving up")
            return
        }
        startTimes.append(now)

        let child = Process()
        child.executableURL = binaryURL
        var arguments = ["-socket", socketPath, "-watch-stdin"]
        if let piExtensionSourceURL {
            arguments += ["-pi-extension-source", piExtensionSourceURL.path, "-pi-extension-autoupdate"]
        }
        if let stateFileURL {
            arguments += ["-state-file", stateFileURL.path]
        }
        if let updateBuildScript {
            arguments += ["-build-script", updateBuildScript]
        }
        for root in petRoots {
            arguments += ["-pets-root", "\(root.source):\(root.url.path)"]
        }
        if let petImportRootURL {
            arguments += ["-pets-import-root", petImportRootURL.path]
        }
        if let catalogDirURL {
            arguments += ["-catalog-dir", catalogDirURL.path]
        }
        if let dialogueHistoryURL {
            arguments += ["-dialogue-history", dialogueHistoryURL.path]
        }
        child.arguments = arguments
        let stdin = Pipe()
        child.standardInput = stdin
        let log = logHandle()
        child.standardOutput = log
        child.standardError = log
        child.terminationHandler = { [weak self] _ in
            self?.queue.async {
                self?.handleTerminationOnQueue()
            }
        }

        do {
            try child.run()
        } catch {
            reportFailure("could not launch pi-pet-daemon: \(error.localizedDescription)")
            return
        }
        process = child
        stdinPipe = stdin
        waitForSocketOnQueue(child: child, deadline: Date().addingTimeInterval(Constants.socketReadyTimeout))
    }

    private func waitForSocketOnQueue(child: Process, deadline: Date) {
        guard !stopping, process === child else { return }
        if FileManager.default.fileExists(atPath: socketPath) {
            notifyReady()
            return
        }
        guard Date() < deadline else {
            reportFailure("pi-pet-daemon did not create socket \(socketPath)")
            child.terminate()
            return
        }
        queue.asyncAfter(deadline: .now() + Constants.socketPollInterval) { [weak self] in
            self?.waitForSocketOnQueue(child: child, deadline: deadline)
        }
    }

    private func handleTerminationOnQueue() {
        process = nil
        stdinPipe = nil
        guard !stopping else { return }
        NSLog("CodexPets: pi-pet-daemon exited unexpectedly; restarting")
        queue.asyncAfter(deadline: .now() + Constants.restartDelay) { [weak self] in
            self?.startOnQueue()
        }
    }

    private func stopOnQueue() {
        stopping = true
        guard let child = process else { return }
        process = nil

        // Closing stdin asks the daemon to shut down gracefully (-watch-stdin),
        // which also removes its socket file.
        try? stdinPipe?.fileHandleForWriting.close()
        stdinPipe = nil

        let deadline = Date().addingTimeInterval(Constants.stopGracePeriod)
        while child.isRunning, Date() < deadline {
            usleep(20_000)
        }
        if child.isRunning {
            child.terminate()
            child.waitUntilExit()
        }
    }

    private func notifyReady() {
        guard let onReady else { return }
        DispatchQueue.main.async {
            onReady()
        }
    }

    private func reportFailure(_ message: String) {
        NSLog("CodexPets: %@", message)
        guard let onFailure else { return }
        DispatchQueue.main.async {
            onFailure(message)
        }
    }

    private func logHandle() -> FileHandle {
        guard let logURL else { return FileHandle.nullDevice }
        let manager = FileManager.default
        try? manager.createDirectory(at: logURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        if !manager.fileExists(atPath: logURL.path) {
            manager.createFile(atPath: logURL.path, contents: nil)
        }
        guard let handle = try? FileHandle(forWritingTo: logURL) else { return FileHandle.nullDevice }
        _ = try? handle.seekToEnd()
        return handle
    }
}
