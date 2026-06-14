import Foundation
import Network

struct DaemonPetRef: Codable, Equatable {
    let id: String
    let slug: String?
    let displayName: String
    let description: String?
    let kind: String?
    let source: String
    let path: String?
    let spritesheetPath: String?
    let frameWidth: Int?
    let frameHeight: Int?
    let license: String?
    let attribution: String?
}

struct DaemonToolRun: Codable, Equatable {
    let id: String
    let sessionId: String
    let name: String
    let state: String
    let safeSummary: String?
}

struct DaemonSession: Codable, Equatable {
    let id: String
    let cwd: String?
    let title: String?
    let status: String
    let safeSummary: String?
    let tools: [DaemonToolRun]?
}

struct DaemonPendingApproval: Codable, Equatable {
    let id: String
    let sessionId: String
    let toolCallId: String?
    let toolName: String
    let commandSummary: String?
    let risk: String?
    let state: String
}

/// Canonical overlay interpretation of a snapshot, computed by the daemon.
/// Native code only renders it.
struct DaemonPresentation: Codable, Equatable {
    let stateId: String
    let bubble: String?
    let autoClearSeconds: Double?
    let activeSessionIds: [String]?

    static let idle = DaemonPresentation(stateId: "idle", bubble: nil, autoClearSeconds: nil, activeSessionIds: nil)

    var autoClearAfter: TimeInterval? {
        guard let autoClearSeconds, autoClearSeconds > 0 else { return nil }
        return autoClearSeconds
    }
}

struct DaemonSnapshot: Codable, Equatable {
    let attention: String
    let sessions: [DaemonSession]
    let pendingApprovals: [DaemonPendingApproval]
    let selectedPetId: String?
    let installedPets: [DaemonPetRef]
    let presentation: DaemonPresentation?
    let update: DaemonUpdateState?

    var presentationOrIdle: DaemonPresentation {
        presentation ?? .idle
    }
}

final class DaemonClient {
    private enum Constants {
        static let version = 1
        static let subscribeMethod = "state.subscribe"
        static let snapshotMethod = "state.snapshot"
        static let snapshotGetMethod = "snapshot.get"
        static let selectPetMethod = "pet.select"
        static let petsRefreshMethod = "pets.refresh"
        static let petsBrowserListMethod = "pets.browser.list"
        static let petImportMethod = "pet.import"
        static let petUninstallMethod = "pet.uninstall"
        static let approvalRespondMethod = "approval.respond"
        static let piExtensionStatusMethod = "pi.extension.status"
        static let piExtensionInstallMethod = "pi.extension.install"
        static let piExtensionUninstallMethod = "pi.extension.uninstall"
        static let updateCheckMethod = "update.check"
        static let updateApplyMethod = "update.apply"
        static let updateDismissMethod = "update.dismiss"
    }

    private let socketPath: String
    private let queue = DispatchQueue(label: "CodexPets.DaemonClient")
    private var subscription: NWConnection?
    private var subscriptionBuffer = Data()
    private var requestCounter = 0
    private var snapshotHandler: ((DaemonSnapshot) -> Void)?
    private var reconnectScheduled = false

    init(socketPath: String = DaemonClient.defaultSocketPath()) {
        self.socketPath = socketPath
    }

    static func defaultSocketPath() -> String {
        if let runtimeDir = ProcessInfo.processInfo.environment["PI_PET_SOCKET_DIR"], !runtimeDir.isEmpty {
            return URL(fileURLWithPath: runtimeDir).appendingPathComponent("pi-pet.sock").path
        }
        if let runtimeDir = ProcessInfo.processInfo.environment["XDG_RUNTIME_DIR"], !runtimeDir.isEmpty {
            return URL(fileURLWithPath: runtimeDir).appendingPathComponent("pi-pet.sock").path
        }
        return URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("codex-pets-\(getuid())", isDirectory: true)
            .appendingPathComponent("pi-pet.sock")
            .path
    }

    func startSnapshotSubscription(onSnapshot: @escaping (DaemonSnapshot) -> Void) {
        queue.async { [weak self] in
            self?.snapshotHandler = onSnapshot
            self?.openSubscriptionOnQueue()
        }
    }

    func stop() {
        queue.async { [weak self] in
            self?.snapshotHandler = nil
            self?.subscription?.cancel()
            self?.subscription = nil
            self?.subscriptionBuffer.removeAll()
        }
    }

    func selectPet(_ petID: String) {
        sendRequest(method: Constants.selectPetMethod, payload: ["petId": petID])
    }

    /// The daemon owns pet storage: it validates, canonicalizes, and writes
    /// the package, then broadcasts the refreshed installed list. Local
    /// packages (bundled catalog pets, picked folders) import by path.
    func importPet(directory: String, completion: @escaping ([String: Any]?) -> Void) {
        sendRequest(method: Constants.petImportMethod, payload: ["directory": directory]) { payload in
            DispatchQueue.main.async {
                completion(payload as? [String: Any])
            }
        }
    }

    /// Merged installed+catalog picker rows computed by the daemon.
    func listBrowserPets(completion: @escaping ([[String: Any]]?) -> Void) {
        sendRequest(method: Constants.petsBrowserListMethod, payload: [:]) { payload in
            DispatchQueue.main.async {
                completion((payload as? [String: Any])?["rows"] as? [[String: Any]])
            }
        }
    }

    func uninstallPet(petID: String, completion: @escaping ([String: Any]?) -> Void) {
        sendRequest(method: Constants.petUninstallMethod, payload: ["petId": petID]) { payload in
            DispatchQueue.main.async {
                completion(payload as? [String: Any])
            }
        }
    }

    func refreshPets() {
        sendRequest(method: Constants.petsRefreshMethod, payload: [:])
    }

    func getSnapshot(completion: @escaping (DaemonSnapshot?) -> Void) {
        sendRequest(method: Constants.snapshotGetMethod, payload: [:]) { payload in
            DispatchQueue.main.async {
                completion(Self.decodeSnapshotPayload(payload))
            }
        }
    }

    func respondToApproval(approvalID: String, decision: String, reason: String? = nil) {
        var payload: [String: Any] = [
            "approvalId": approvalID,
            "decision": decision,
        ]
        if let reason, !reason.isEmpty {
            payload["reason"] = reason
        }
        sendRequest(method: Constants.approvalRespondMethod, payload: payload)
    }

    /// Asks the daemon's brain how the pet reacts to an interaction. Only
    /// the murmur text comes back (the shared phrase book lives in the
    /// daemon); the pose reaction is rendered locally.
    func sendInteraction(type: String, clicks: Int = 0, onMurmur: @escaping (String) -> Void) {
        var payload: [String: Any] = ["type": type]
        if clicks > 0 {
            payload["clicks"] = clicks
        }
        sendRequest(method: "overlay.interaction", payload: payload) { result in
            guard
                let result = result as? [String: Any],
                let murmur = result["murmur"] as? String,
                !murmur.isEmpty
            else {
                return
            }
            DispatchQueue.main.async {
                onMurmur(murmur)
            }
        }
    }

    func setOverlaySettings(attentionMode: String, bubbleMode: String, reduceMotion: Bool) {
        sendRequest(method: "overlay.settings.set", payload: [
            "attentionMode": attentionMode,
            "bubbleMode": bubbleMode,
            "reduceMotion": reduceMotion,
        ])
    }

    /// Mutes murmurs for the rest of the day (nil) or for seconds.
    func muteMurmurs(seconds: TimeInterval?) {
        if let seconds {
            sendRequest(method: "murmurs.mute", payload: ["seconds": seconds])
        } else {
            sendRequest(method: "murmurs.mute", payload: ["today": true])
        }
    }

    func checkForUpdates() {
        sendRequest(method: Constants.updateCheckMethod, payload: [:])
    }

    func applyUpdate() {
        sendRequest(method: Constants.updateApplyMethod, payload: [:])
    }

    func dismissUpdateFailure() {
        sendRequest(method: Constants.updateDismissMethod, payload: [:])
    }

    func getPiExtensionStatus(completion: @escaping ([String: Any]?) -> Void) {
        requestPiExtension(method: Constants.piExtensionStatusMethod, completion: completion)
    }

    func installPiExtension(completion: @escaping ([String: Any]?) -> Void) {
        requestPiExtension(method: Constants.piExtensionInstallMethod, completion: completion)
    }

    func uninstallPiExtension(completion: @escaping ([String: Any]?) -> Void) {
        requestPiExtension(method: Constants.piExtensionUninstallMethod, completion: completion)
    }

    private func requestPiExtension(method: String, completion: @escaping ([String: Any]?) -> Void) {
        sendRequest(method: method, payload: [:]) { payload in
            DispatchQueue.main.async {
                completion(payload as? [String: Any])
            }
        }
    }

    private func openSubscriptionOnQueue() {
        guard snapshotHandler != nil else { return }
        subscription?.cancel()
        subscriptionBuffer.removeAll()

        let connection = NWConnection(to: .unix(path: socketPath), using: .tcp)
        subscription = connection
        connection.stateUpdateHandler = { [weak self, weak connection] state in
            guard let self, let connection else { return }
            switch state {
            case .ready:
                self.send(message: self.request(method: Constants.subscribeMethod, payload: [:]), on: connection)
                self.receive(on: connection)
            case .failed:
                connection.cancel()
                self.scheduleSubscriptionRetryOnQueue(for: connection)
            default:
                break
            }
        }
        connection.start(queue: queue)
    }

    private func scheduleSubscriptionRetryOnQueue(for connection: NWConnection) {
        guard subscription === connection, snapshotHandler != nil, !reconnectScheduled else { return }
        reconnectScheduled = true
        queue.asyncAfter(deadline: .now() + 1) { [weak self] in
            guard let self else { return }
            self.reconnectScheduled = false
            self.openSubscriptionOnQueue()
        }
    }

    private func sendRequest(method: String, payload: [String: Any], onPayload: ((Any?) -> Void)? = nil) {
        queue.async { [weak self] in
            guard let self else { return }
            let connection = NWConnection(to: .unix(path: self.socketPath), using: .tcp)
            connection.stateUpdateHandler = { [weak self, weak connection] state in
                guard let self, let connection else { return }
                switch state {
                case .ready:
                    self.send(message: self.request(method: method, payload: payload), on: connection)
                    connection.receive(minimumIncompleteLength: 1, maximumLength: 256 * 1024) { data, _, _, _ in
                        if let onPayload {
                            onPayload(data.flatMap(Self.decodeResponsePayload))
                        }
                        connection.cancel()
                    }
                case .failed:
                    connection.cancel()
                    onPayload?(nil)
                default:
                    break
                }
            }
            connection.start(queue: self.queue)
        }
    }

    private func receive(on connection: NWConnection) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 256 * 1024) { [weak self, weak connection] data, _, isComplete, error in
            guard let self, let connection else { return }
            if let data, !data.isEmpty {
                self.subscriptionBuffer.append(data)
                self.consumeSnapshotLines()
            }
            if isComplete || error != nil {
                connection.cancel()
                self.scheduleSubscriptionRetryOnQueue(for: connection)
                return
            }
            self.receive(on: connection)
        }
    }

    private func consumeSnapshotLines() {
        while let newline = subscriptionBuffer.firstIndex(of: 0x0a) {
            let line = subscriptionBuffer[..<newline]
            subscriptionBuffer.removeSubrange(...newline)
            guard
                let snapshot = Self.decodeSnapshotEnvelope(Data(line)),
                let onSnapshot = snapshotHandler
            else { continue }
            DispatchQueue.main.async {
                onSnapshot(snapshot)
            }
        }
    }

    private func request(method: String, payload: [String: Any]) -> [String: Any] {
        requestCounter += 1
        return [
            "version": Constants.version,
            "kind": "request",
            "id": "mac-\(requestCounter)",
            "method": method,
            "payload": payload,
        ]
    }

    private func send(message: [String: Any], on connection: NWConnection) {
        guard let data = try? JSONSerialization.data(withJSONObject: message, options: []) else { return }
        var line = data
        line.append(0x0a)
        connection.send(content: line, completion: .contentProcessed { _ in })
    }

    private static func decodeSnapshotEnvelope(_ data: Data) -> DaemonSnapshot? {
        guard
            let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let method = object["method"] as? String,
            method == Constants.snapshotMethod || method == Constants.subscribeMethod,
            let payload = object["payload"],
            JSONSerialization.isValidJSONObject(payload),
            let payloadData = try? JSONSerialization.data(withJSONObject: payload, options: [])
        else {
            return nil
        }

        let decoder = JSONDecoder()
        return try? decoder.decode(DaemonSnapshot.self, from: payloadData)
    }

    private static func decodeResponsePayload(_ data: Data) -> Any? {
        guard
            let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            object["error"] == nil
        else {
            return nil
        }
        return object["payload"]
    }

    private static func decodeSnapshotPayload(_ payload: Any?) -> DaemonSnapshot? {
        guard
            let payload,
            JSONSerialization.isValidJSONObject(payload),
            let payloadData = try? JSONSerialization.data(withJSONObject: payload, options: [])
        else {
            return nil
        }
        return try? JSONDecoder().decode(DaemonSnapshot.self, from: payloadData)
    }

}
