import Cocoa
import Foundation

/// View-model for the overlay's update button. The update pipeline itself
/// (git check, pull, build, commit marker) runs in the shared Go daemon;
/// native code only renders this status and relaunches the app when asked.
enum UpdateStatus: Equatable {
    case idle
    case checking
    case available(commitsBehind: Int)
    case updating(stage: String)
    case failed(message: String)
    case restarting
}

/// Wire form of the daemon's snapshot `update` field.
struct DaemonUpdateState: Codable, Equatable {
    let available: Bool?
    let commitsBehind: Int?
    let stage: String?
    let message: String?
}

extension UpdateStatus {
    static func fromDaemon(_ state: DaemonUpdateState?) -> UpdateStatus {
        guard let state else { return .idle }
        switch state.stage ?? "" {
        case "checking":
            return .checking
        case "fetching":
            return .updating(stage: "Fetching…")
        case "pulling":
            return .updating(stage: "Pulling…")
        case "building":
            return .updating(stage: "Building…")
        case "restartPending":
            return .restarting
        case "failed":
            return .failed(message: state.message ?? "Update failed")
        default:
            if state.available == true {
                return .available(commitsBehind: state.commitsBehind ?? 0)
            }
            return .idle
        }
    }
}

/// The only platform-specific piece of the update flow: relaunch the app
/// bundle after the daemon finished building the new version.
enum AppRelauncher {
    static func relaunch() {
        let bundlePath = detectAppBundlePath()

        // Schedule the relaunch after we exit, so the old process fully
        // releases the daemon socket before the new one tries to bind.
        let script = """
        sleep 1
        open -a '\(bundlePath.replacingOccurrences(of: "'", with: "'\\''"))'
        """
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/sh")
        task.arguments = ["-c", script]
        task.standardOutput = FileHandle.nullDevice
        task.standardError = FileHandle.nullDevice
        try? task.run()

        DispatchQueue.main.async {
            NSApplication.shared.terminate(nil)
        }
    }

    private static func detectAppBundlePath() -> String {
        var url = URL(fileURLWithPath: Bundle.main.executablePath ?? ProcessInfo.processInfo.arguments[0])
        for _ in 0..<10 {
            if url.pathExtension == "app" {
                return url.path
            }
            url = url.deletingLastPathComponent()
        }
        return Bundle.main.bundlePath
    }
}
