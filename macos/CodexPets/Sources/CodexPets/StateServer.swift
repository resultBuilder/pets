import Foundation
import Network

final class StateServer {
    typealias StateHandler = (_ state: String, _ duration: TimeInterval?) -> Void
    typealias BubbleHandler = (_ text: String) -> Void
    typealias EventHandler = (_ type: String, _ label: String?, _ importance: PetEventImportance) -> Void

    private let runtimeRoot: URL
    private let onState: StateHandler
    private let onBubble: BubbleHandler
    private let onEvent: EventHandler
    private var listener: NWListener?
    private var runningToggle = false

    private(set) var port: UInt16 = 7777
    let token: String

    static var isDebugEnabled: Bool {
        let value = ProcessInfo.processInfo.environment["CODEX_PETS_ENABLE_HTTP_STATE_API"]?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
        return value == "1" || value == "true" || value == "yes" || value == "on"
    }

    init(
        runtimeRoot: URL,
        onState: @escaping StateHandler,
        onBubble: @escaping BubbleHandler,
        onEvent: @escaping EventHandler
    ) {
        self.runtimeRoot = runtimeRoot
        self.onState = onState
        self.onBubble = onBubble
        self.onEvent = onEvent
        self.token = UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()
    }

    func start() {
        guard Self.isDebugEnabled else { return }

        let requested = UInt16(ProcessInfo.processInfo.environment["CODEX_PETS_PORT"] ?? "7777") ?? 7777
        start(on: requested) { [weak self] success in
            guard let self, !success else { return }
            self.start(on: requested == 7777 ? 7778 : 7777) { _ in }
        }
    }

    func copyCurlSnippet() -> String {
        """
        curl -X POST http://127.0.0.1:\(port)/state \\
          -H 'content-type: application/json' \\
          -H 'x-codex-pets-token: \(token)' \\
          -d '{"state":"running","duration":1200}'
        """
    }

    private func start(on port: UInt16, completion: @escaping (Bool) -> Void) {
        do {
            let listener = try NWListener(using: .tcp, on: NWEndpoint.Port(rawValue: port)!)
            self.listener = listener
            self.port = port
            listener.newConnectionHandler = { [weak self] connection in
                self?.handle(connection)
            }
            listener.stateUpdateHandler = { [weak self] state in
                switch state {
                case .ready:
                    self?.persistRuntimeInfo()
                    completion(true)
                case .failed:
                    completion(false)
                default:
                    break
                }
            }
            listener.start(queue: .global(qos: .utility))
        } catch {
            completion(false)
        }
    }

    private func persistRuntimeInfo() {
        try? FileManager.default.createDirectory(at: runtimeRoot, withIntermediateDirectories: true)
        let tokenURL = runtimeRoot.appendingPathComponent("state-token")
        let infoURL = runtimeRoot.appendingPathComponent("state-api.json")
        try? token.write(to: tokenURL, atomically: true, encoding: .utf8)
        try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: tokenURL.path)
        let info: [String: Any] = [
            "port": Int(port),
            "tokenPath": tokenURL.path,
            "stateUrl": "http://127.0.0.1:\(port)/state",
            "bubbleUrl": "http://127.0.0.1:\(port)/bubble",
            "eventUrl": "http://127.0.0.1:\(port)/event",
        ]
        if let data = try? JSONSerialization.data(withJSONObject: info, options: [.prettyPrinted, .sortedKeys]) {
            try? data.write(to: infoURL)
        }
    }

    private func handle(_ connection: NWConnection) {
        connection.start(queue: .global(qos: .utility))
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, _, _ in
            guard let self else {
                connection.cancel()
                return
            }
            let response: HTTPResponse
            if let data, let request = HTTPRequest(data: data) {
                response = self.route(request)
            } else {
                response = .json(status: 400, ["ok": false, "error": "invalid_request"])
            }
            connection.send(content: response.data, completion: .contentProcessed { _ in
                connection.cancel()
            })
        }
    }

    private func route(_ request: HTTPRequest) -> HTTPResponse {
        if request.method == "GET", request.path == "/health" {
            return .json(status: 200, ["ok": true, "port": Int(port)])
        }

        if request.method == "GET", request.path == "/state" {
            return .json(status: 200, ["ok": true, "valid": Array(validStates).sorted()])
        }

        if request.method == "GET", request.path == "/bubble" {
            return .json(status: 200, ["ok": true])
        }

        if request.method == "GET", request.path == "/event" {
            return .json(status: 200, ["ok": true, "types": ["task.succeeded", "task.needs_user", "review", "task.failed"]])
        }

        guard request.method == "POST", request.path == "/state" || request.path == "/bubble" || request.path == "/event" else {
            return .json(status: 404, ["ok": false, "error": "not_found"])
        }

        guard request.headers["x-codex-pets-token"] == token else {
            return .json(status: 401, [
                "ok": false,
                "error": "unauthorized",
                "tokenPath": runtimeRoot.appendingPathComponent("state-token").path,
            ])
        }

        guard let body = request.jsonBody else {
            return .json(status: 400, ["ok": false, "error": "invalid_json"])
        }

        if request.path == "/bubble" {
            let text = (body["text"] as? String ?? "").prefix(200)
            DispatchQueue.main.async { [onBubble] in
                onBubble(String(text))
            }
            return .json(status: 200, ["ok": true, "text": String(text)])
        }

        if request.path == "/event" {
            guard let type = (body["type"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines), !type.isEmpty else {
                return .json(status: 400, ["ok": false, "error": "missing_event_type"])
            }
            let rawLabel = (body["label"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines)
            let label = rawLabel?.isEmpty == false ? rawLabel : nil
            let importance = PetEventImportance(raw: body["importance"] as? String)
            DispatchQueue.main.async { [onEvent] in
                onEvent(type, label, importance)
            }
            return .json(status: 200, [
                "ok": true,
                "type": type,
                "label": label ?? NSNull(),
                "importance": importance.rawValue,
            ])
        }

        guard var state = body["state"] as? String, validStates.contains(state) else {
            return .json(status: 400, ["ok": false, "error": "invalid_state", "valid": Array(validStates).sorted()])
        }
        if state == "running" {
            runningToggle.toggle()
            state = runningToggle ? "running-left" : "running-right"
        }

        let durationMs = body["duration"] as? Double
        let duration = durationMs.map { min(max($0, 0), 30_000) / 1000.0 }
        DispatchQueue.main.async { [onState] in
            onState(state, duration)
        }
        return .json(status: 200, ["ok": true, "state": state, "duration": durationMs ?? NSNull()])
    }

    private var validStates: Set<String> {
        Set(PetAnimationState.defaults.map(\.id)).union(["running"])
    }
}

private struct HTTPRequest {
    let method: String
    let path: String
    let headers: [String: String]
    let jsonBody: [String: Any]?

    init?(data: Data) {
        guard let raw = String(data: data, encoding: .utf8) else { return nil }
        let parts = raw.components(separatedBy: "\r\n\r\n")
        guard let head = parts.first else { return nil }
        let body = parts.dropFirst().joined(separator: "\r\n\r\n")
        let lines = head.components(separatedBy: "\r\n")
        guard let requestLine = lines.first else { return nil }
        let requestBits = requestLine.split(separator: " ", maxSplits: 2).map(String.init)
        guard requestBits.count >= 2 else { return nil }

        self.method = requestBits[0].uppercased()
        self.path = URL(string: requestBits[1])?.path ?? requestBits[1]

        var parsedHeaders: [String: String] = [:]
        for line in lines.dropFirst() {
            guard let colon = line.firstIndex(of: ":") else { continue }
            let key = line[..<colon].trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespacesAndNewlines)
            parsedHeaders[key] = value
        }
        self.headers = parsedHeaders

        if body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            self.jsonBody = [:]
        } else if
            let bodyData = body.data(using: .utf8),
            let object = try? JSONSerialization.jsonObject(with: bodyData) as? [String: Any]
        {
            self.jsonBody = object
        } else {
            self.jsonBody = nil
        }
    }
}

private struct HTTPResponse {
    let data: Data

    static func json(status: Int, _ body: [String: Any]) -> HTTPResponse {
        let payload = (try? JSONSerialization.data(withJSONObject: body)) ?? Data("{}".utf8)
        let reason: String
        switch status {
        case 200: reason = "OK"
        case 400: reason = "Bad Request"
        case 401: reason = "Unauthorized"
        case 404: reason = "Not Found"
        default: reason = "OK"
        }
        var head = ""
        head += "HTTP/1.1 \(status) \(reason)\r\n"
        head += "Content-Type: application/json; charset=utf-8\r\n"
        head += "Content-Length: \(payload.count)\r\n"
        head += "Connection: close\r\n\r\n"
        var response = Data(head.utf8)
        response.append(payload)
        return HTTPResponse(data: response)
    }
}
