import Cocoa
import Foundation
import WebKit

private final class WeakScriptMessageHandler: NSObject, WKScriptMessageHandler {
    weak var delegate: WKScriptMessageHandler?

    init(delegate: WKScriptMessageHandler) {
        self.delegate = delegate
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        delegate?.userContentController(userContentController, didReceive: message)
    }
}

private final class PetdexBrowserWindow: NSWindow {
    override func sendEvent(_ event: NSEvent) {
        if event.type == .keyDown, isCloseKeyEquivalent(event) {
            performClose(nil)
            return
        }
        super.sendEvent(event)
    }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if isCloseKeyEquivalent(event) {
            performClose(nil)
            return true
        }
        return super.performKeyEquivalent(with: event)
    }

    private func isCloseKeyEquivalent(_ event: NSEvent) -> Bool {
        let flags = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
        return flags == .command && event.charactersIgnoringModifiers?.lowercased() == "w"
    }
}

enum PetdexBrowserBridgeAction: String, CaseIterable {
    case importPet
    case listBrowserPets
    case listInstalledPets
    case selectInstalledPet
    case installPiExtension
    case uninstallPiExtension
    case getPiExtensionStatus
}

/// Serves installed-pet spritesheets to the WebView on demand. This replaces
/// inlining spritesheets as multi-megabyte base64 data URLs, which had to be
/// read and encoded on the main thread and re-parsed by WebKit on every
/// list refresh. Only spritesheets of known installed pets are served.
final class PetAssetSchemeHandler: NSObject, WKURLSchemeHandler {
    static let scheme = "codexpets-asset"

    var spritesheetResolver: ((String) -> URL?)?
    private let readQueue = DispatchQueue(label: "CodexPets.PetAssets", qos: .userInitiated)
    private var liveTasks = Set<ObjectIdentifier>()

    func webView(_ webView: WKWebView, start urlSchemeTask: WKURLSchemeTask) {
        let taskID = ObjectIdentifier(urlSchemeTask)
        liveTasks.insert(taskID)
        guard
            let url = urlSchemeTask.request.url,
            let petID = Self.petID(fromAssetURL: url),
            let fileURL = spritesheetResolver?(petID)
        else {
            liveTasks.remove(taskID)
            urlSchemeTask.didFailWithError(URLError(.fileDoesNotExist))
            return
        }

        readQueue.async { [weak self] in
            let data = try? Data(contentsOf: fileURL)
            DispatchQueue.main.async {
                guard let self, self.liveTasks.remove(taskID) != nil else { return }
                guard let data else {
                    urlSchemeTask.didFailWithError(URLError(.cannotOpenFile))
                    return
                }
                let response = HTTPURLResponse(
                    url: url,
                    statusCode: 200,
                    httpVersion: "HTTP/1.1",
                    headerFields: [
                        "Content-Type": Self.mimeType(forExtension: fileURL.pathExtension),
                        "Content-Length": String(data.count),
                        "Cache-Control": "max-age=600",
                    ]
                )
                guard let response else {
                    urlSchemeTask.didFailWithError(URLError(.badServerResponse))
                    return
                }
                urlSchemeTask.didReceive(response)
                urlSchemeTask.didReceive(data)
                urlSchemeTask.didFinish()
            }
        }
    }

    func webView(_ webView: WKWebView, stop urlSchemeTask: WKURLSchemeTask) {
        liveTasks.remove(ObjectIdentifier(urlSchemeTask))
    }

    static func assetURLString(for pet: PetPackage) -> String {
        assetURLString(petID: pet.id, spritesheetFileName: pet.spritesheet.lastPathComponent)
    }

    static func assetURLString(petID: String, spritesheetFileName: String) -> String {
        let encoded = petID.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
        let ext = (spritesheetFileName as NSString).pathExtension.lowercased()
        return "\(scheme):///pet/\(encoded)/spritesheet.\(ext.isEmpty ? "png" : ext)"
    }

    static func petID(fromAssetURL url: URL) -> String? {
        // URL.path percent-decodes, which would split pet ids containing "/";
        // parse the still-encoded path instead.
        guard let encodedPath = URLComponents(url: url, resolvingAgainstBaseURL: false)?.percentEncodedPath else {
            return nil
        }
        let parts = encodedPath.split(separator: "/").map(String.init)
        guard parts.count >= 2, parts[0] == "pet" else { return nil }
        return parts[1].removingPercentEncoding
    }

    static func mimeType(forExtension ext: String) -> String {
        switch ext.lowercased() {
        case "png": return "image/png"
        case "webp": return "image/webp"
        default: return "application/octet-stream"
        }
    }
}

final class PetdexBrowserWindowController: NSWindowController, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    private enum Constants {
        static let bridgeName = "codexPets"
        static let browserResourceDirectory = "PetdexBrowser"
        static let initialSize = NSRect(x: 0, y: 0, width: 980, height: 640)
        static let minimumSize = NSSize(width: 820, height: 560)
        static var backgroundColor: NSColor { .windowBackgroundColor }
    }

    private let daemonClient: DaemonClient?
    private let onImport: (String) -> Void
    private let installedPetsProvider: () -> [PetPackage]
    private let onSelectInstalled: (PetPackage) -> Void
    private let onBrowserLoaded: (([String: Any]) -> Void)?
    private let onClose: (() -> Void)?
    private let webView: WKWebView
    private let loadingView = NSView(frame: .zero)
    private let statusLabel = NSTextField(labelWithString: "Loading bundled pets...")

    private var didStartLoading = false
    private var didFinishInitialLoad = false
    private var importInFlight = false
    private var didClose = false
    private var scriptMessageHandler: WeakScriptMessageHandler?
    /// Last pets.browser.list rows; resolves catalog spritesheets and
    /// import-by-id requests.
    private var browserRows: [[String: Any]] = []

    init(
        daemonClient: DaemonClient? = nil,
        installedPetsProvider: (() -> [PetPackage])? = nil,
        onImport: @escaping (String) -> Void = { _ in },
        onSelectInstalled: ((PetPackage) -> Void)? = nil,
        onBrowserLoaded: (([String: Any]) -> Void)? = nil,
        onClose: (() -> Void)? = nil
    ) {
        self.daemonClient = daemonClient
        self.onImport = onImport
        let petsProvider = installedPetsProvider ?? { [] }
        self.installedPetsProvider = petsProvider
        self.onSelectInstalled = onSelectInstalled ?? { _ in }
        self.onBrowserLoaded = onBrowserLoaded
        self.onClose = onClose
        let assetHandler = PetAssetSchemeHandler()
        self.webView = WKWebView(frame: .zero, configuration: Self.makeWebViewConfiguration(assetHandler: assetHandler))
        let window = PetdexBrowserWindow(
            contentRect: Constants.initialSize,
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Petdex"
        window.minSize = Constants.minimumSize
        window.backgroundColor = Constants.backgroundColor
        window.isReleasedWhenClosed = true
        super.init(window: window)
        window.delegate = self

        // The scheme handler serves every row's spritesheet — installed
        // packages and bundled catalog pets alike — by daemon pet id.
        assetHandler.spritesheetResolver = { [weak self] petID in
            if let installed = petsProvider().first(where: { $0.id == petID }) {
                return installed.spritesheet
            }
            return self?.browserRowSpritesheetURL(petID: petID)
        }

        let handler = WeakScriptMessageHandler(delegate: self)
        scriptMessageHandler = handler
        webView.configuration.userContentController.add(handler, name: Constants.bridgeName)
        setupUI()
        prepareForDisplay()
    }

    required init?(coder: NSCoder) {
        nil
    }

    deinit {
        cleanupWebView()
    }

    func windowWillClose(_ notification: Notification) {
        cleanupWebView()
        DispatchQueue.main.async { [onClose] in
            onClose?()
        }
    }

    func prepareForDisplay() {
        loadBrowserIfNeeded()
    }

    override func showWindow(_ sender: Any?) {
        guard !didClose else { return }
        prepareForDisplay()
        super.showWindow(sender)
        if didFinishInitialLoad {
            revealWebView()
        }
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.name == Constants.bridgeName,
              let body = message.body as? [String: Any],
              let actionName = body["action"] as? String,
              let action = PetdexBrowserBridgeAction(rawValue: actionName)
        else {
            return
        }

        switch action {
        case .importPet:
            importPet(petID: body["petId"])
        case .listBrowserPets:
            sendBrowserPets()
        case .listInstalledPets:
            sendInstalledPets()
        case .selectInstalledPet:
            selectInstalledPet(from: body["petId"])
        case .installPiExtension:
            installPiExtension()
        case .uninstallPiExtension:
            uninstallPiExtension()
        case .getPiExtensionStatus:
            sendPiExtensionStatus()
        }
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        didFinishInitialLoad = true
        revealWebView()
        notifyNativeReady()
        reportBrowserLoadedForSmoke()
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        showLoadingStatus(error.localizedDescription)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        showLoadingStatus(error.localizedDescription)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        didStartLoading = false
        didFinishInitialLoad = false
        webView.alphaValue = 0
        loadingView.isHidden = false
        showLoadingStatus("Reloading bundled pets...")
        loadBrowserIfNeeded()
    }

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
    ) {
        if navigationAction.navigationType == .linkActivated,
           let url = navigationAction.request.url,
           Self.shouldOpenExternally(url)
        {
            NSWorkspace.shared.open(url)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(
        _ webView: WKWebView,
        createWebViewWith configuration: WKWebViewConfiguration,
        for navigationAction: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        if navigationAction.targetFrame == nil,
           let url = navigationAction.request.url,
           Self.shouldOpenExternally(url)
        {
            NSWorkspace.shared.open(url)
        }
        return nil
    }

    private static func makeWebViewConfiguration(assetHandler: PetAssetSchemeHandler) -> WKWebViewConfiguration {
        let configuration = WKWebViewConfiguration()
        configuration.setURLSchemeHandler(assetHandler, forURLScheme: PetAssetSchemeHandler.scheme)
        let userContentController = WKUserContentController()
        let bootstrap = """
        (() => {
          const postMessage = (payload) => {
            window.webkit?.messageHandlers?.\(Constants.bridgeName)?.postMessage(payload);
          };
          Object.defineProperty(window, "CodexPetsNative", {
            configurable: true,
            value: { isNativeShell: true, postMessage }
          });
          document.documentElement.classList.add("native-shell");
          const notifyReady = () => {
            window.dispatchEvent(new CustomEvent("codex-pets-native-ready"));
          };
          if (document.readyState === "loading") {
            document.addEventListener("DOMContentLoaded", notifyReady, { once: true });
          } else {
            notifyReady();
          }
        })();
        """
        userContentController.addUserScript(WKUserScript(source: bootstrap, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        configuration.userContentController = userContentController
        configuration.websiteDataStore = .nonPersistent()
        configuration.suppressesIncrementalRendering = true
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        return configuration
    }

    private func setupUI() {
        guard let contentView = window?.contentView else { return }

        contentView.wantsLayer = true

        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.alphaValue = 0
        webView.translatesAutoresizingMaskIntoConstraints = false
        webView.wantsLayer = true
        contentView.addSubview(webView)

        loadingView.wantsLayer = true
        loadingView.translatesAutoresizingMaskIntoConstraints = false
        contentView.addSubview(loadingView)

        statusLabel.alignment = .center
        statusLabel.textColor = .secondaryLabelColor
        statusLabel.translatesAutoresizingMaskIntoConstraints = false
        loadingView.addSubview(statusLabel)

        NSLayoutConstraint.activate([
            webView.leadingAnchor.constraint(equalTo: contentView.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: contentView.trailingAnchor),
            webView.topAnchor.constraint(equalTo: contentView.topAnchor),
            webView.bottomAnchor.constraint(equalTo: contentView.bottomAnchor),
            loadingView.leadingAnchor.constraint(equalTo: contentView.leadingAnchor),
            loadingView.trailingAnchor.constraint(equalTo: contentView.trailingAnchor),
            loadingView.topAnchor.constraint(equalTo: contentView.topAnchor),
            loadingView.bottomAnchor.constraint(equalTo: contentView.bottomAnchor),
            statusLabel.centerXAnchor.constraint(equalTo: loadingView.centerXAnchor),
            statusLabel.centerYAnchor.constraint(equalTo: loadingView.centerYAnchor),
            statusLabel.leadingAnchor.constraint(greaterThanOrEqualTo: loadingView.leadingAnchor, constant: 24),
            statusLabel.trailingAnchor.constraint(lessThanOrEqualTo: loadingView.trailingAnchor, constant: -24),
        ])

        applyNativeThemeBackground()
    }

    private func cleanupWebView() {
        guard !didClose else { return }
        didClose = true
        webView.stopLoading()
        webView.navigationDelegate = nil
        webView.uiDelegate = nil
        webView.configuration.userContentController.removeScriptMessageHandler(forName: Constants.bridgeName)
        webView.removeFromSuperview()
        scriptMessageHandler = nil
    }

    private func applyNativeThemeBackground() {
        let background = Constants.backgroundColor
        window?.backgroundColor = background
        window?.contentView?.layer?.backgroundColor = background.cgColor
        webView.layer?.backgroundColor = background.cgColor
        loadingView.layer?.backgroundColor = background.cgColor
        if #available(macOS 11.0, *) {
            webView.underPageBackgroundColor = background
        }
    }

    private func showLoadingStatus(_ message: String) {
        statusLabel.stringValue = message
    }

    private func loadBrowserIfNeeded() {
        guard !didStartLoading else { return }
        didStartLoading = true
        showLoadingStatus("Loading bundled pets...")

        if let indexURL = Self.browserIndexURL() {
            webView.loadFileURL(indexURL, allowingReadAccessTo: indexURL.deletingLastPathComponent())
        } else {
            let html = """
            <!doctype html>
            <html><body style="margin:0;background:#f5f7f8;color:#182427;font:14px -apple-system, BlinkMacSystemFont, sans-serif;display:grid;min-height:100vh;place-items:center">
            <p>Petdex browser assets were not found in the app bundle.</p>
            </body></html>
            """
            webView.loadHTMLString(html, baseURL: nil)
        }
    }

    private func revealWebView() {
        loadingView.isHidden = true
        webView.alphaValue = 1
    }

    private func notifyNativeReady() {
        evaluateJavaScriptEvent(
            name: "codex-pets-native-ready",
            detail: [:]
        )
        sendInstalledPets()
        sendBrowserPets()
        sendPiExtensionStatus()
    }

    private func reportBrowserLoadedForSmoke() {
        guard let onBrowserLoaded else { return }
        let script = """
        (() => ({
          event: "petdexBrowser",
          bootStarted: Boolean(window.__codexPetsAppBoot?.started),
          bootLoaded: Boolean(window.__codexPetsAppBoot?.loaded),
          bootError: window.__codexPetsAppBoot?.error || "",
          rowCount: document.querySelectorAll(".pet-row").length,
          status: document.querySelector("#status")?.textContent?.trim() || "",
          selectedName: document.querySelector("#pet-name")?.textContent?.trim() || ""
        }))()
        """
        DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(250)) { [weak self] in
            self?.webView.evaluateJavaScript(script) { value, _ in
                guard let payload = value as? [String: Any] else { return }
                onBrowserLoaded(payload)
            }
        }
    }

    // The daemon already knows every catalog row (it scanned the bundled
    // catalog itself), so importing is a pet-id lookup plus pet.import with
    // the package directory — no downloading, merging, or encoding here.
    private func importPet(petID: Any?) {
        guard !importInFlight else {
            evaluateImportResult(ok: false, message: "Another import is already running")
            return
        }
        guard let daemonClient else {
            evaluateImportResult(ok: false, message: "Pet daemon is not available")
            return
        }
        guard
            let id = Self.string(petID),
            let row = browserRows.first(where: { $0["id"] as? String == id }),
            let directory = row["path"] as? String,
            !directory.isEmpty
        else {
            evaluateImportResult(ok: false, message: "Selected pet was not found in the catalog")
            return
        }

        importInFlight = true
        daemonClient.importPet(directory: directory) { [weak self] payload in
            guard let self else { return }
            self.importInFlight = false
            let ok = payload?["ok"] as? Bool ?? false
            let message = payload?["message"] as? String
                ?? (payload == nil ? "Pet daemon is not available" : "Import failed")
            self.evaluateImportResult(ok: ok, message: message)
            guard ok, let pet = payload?["pet"] as? [String: Any] else { return }
            if let importedID = pet["id"] as? String {
                daemonClient.selectPet(importedID)
            }
            self.onImport(pet["displayName"] as? String ?? (row["displayName"] as? String ?? ""))
        }
    }

    private func evaluateImportResult(ok: Bool, message: String) {
        evaluateJavaScriptEvent(
            name: "codex-pets-native-import-result",
            detail: [
                "ok": ok,
                "message": message,
            ]
        )
    }

    /// Pushes the current pet lists into the page. The app calls this when
    /// a daemon snapshot changes the installed pets.
    func refreshInstalledPets() {
        guard didFinishInitialLoad, !didClose else { return }
        sendInstalledPets()
        sendBrowserPets()
    }

    private func sendInstalledPets() {
        let pets = installedPetsProvider().map(Self.installedPetPayload)
        evaluateJavaScriptEvent(
            name: "codex-pets-native-installed-pets",
            detail: ["pets": pets]
        )
    }

    /// Sends the daemon-merged installed+catalog rows. The page renders them
    /// verbatim; all merging and dedup-hiding happens in the daemon.
    private func sendBrowserPets() {
        guard let daemonClient else { return }
        daemonClient.listBrowserPets { [weak self] rows in
            guard let self, let rows else { return }
            self.browserRows = rows
            let pets = rows.compactMap(Self.browserRowPayload)
            self.evaluateJavaScriptEvent(
                name: "codex-pets-native-browser-pets",
                detail: ["pets": pets]
            )
        }
    }

    private static func browserRowPayload(_ row: [String: Any]) -> [String: Any]? {
        guard let id = row["id"] as? String, !id.isEmpty else { return nil }
        let installed = row["installed"] as? Bool ?? false
        let spriteName = row["spritesheetPath"] as? String ?? "spritesheet.webp"
        return [
            "source": installed ? "installed" : "prebundled",
            "nativePetId": id,
            "slug": row["slug"] as? String ?? "",
            "displayName": row["displayName"] as? String ?? "",
            "description": row["description"] as? String ?? "",
            "kind": row["kind"] as? String ?? "pet",
            "submittedBy": installed
                ? installedSourceLabel(row["source"] as? String ?? "")
                : (row["attribution"] as? String ?? ""),
            "tags": [String](),
            "spritesheetUrl": PetAssetSchemeHandler.assetURLString(petID: id, spritesheetFileName: spriteName),
            "frameWidth": row["frameWidth"] as? Int ?? 192,
            "frameHeight": row["frameHeight"] as? Int ?? 208,
            "canUninstall": row["canUninstall"] as? Bool ?? false,
            "installed": installed,
        ]
    }

    private static func installedSourceLabel(_ source: String) -> String {
        PetSource(rawValue: source)?.label ?? source
    }

    private func browserRowSpritesheetURL(petID: String) -> URL? {
        guard
            let row = browserRows.first(where: { $0["id"] as? String == petID }),
            let path = row["path"] as? String, !path.isEmpty,
            let spriteName = row["spritesheetPath"] as? String, !spriteName.isEmpty
        else {
            return nil
        }
        return URL(fileURLWithPath: path, isDirectory: true).appendingPathComponent(spriteName)
    }

    private func selectInstalledPet(from payload: Any?) {
        guard let pet = installedPet(for: payload) else {
            evaluateImportResult(ok: false, message: "Installed pet was not found")
            return
        }
        onSelectInstalled(pet)
        evaluateImportResult(ok: true, message: "Selected \(pet.displayName)")
    }

    // Pi extension management lives in the daemon (pi.extension.* protocol
    // methods); the browser only relays results into WebView events.
    private func installPiExtension() {
        relayPiExtensionRequest(eventName: "codex-pets-native-pi-install-result") { daemon, completion in
            daemon.installPiExtension(completion: completion)
        }
    }

    private func uninstallPiExtension() {
        relayPiExtensionRequest(eventName: "codex-pets-native-pi-uninstall-result") { daemon, completion in
            daemon.uninstallPiExtension(completion: completion)
        }
    }

    private func sendPiExtensionStatus() {
        relayPiExtensionRequest(eventName: "codex-pets-native-pi-install-status") { daemon, completion in
            daemon.getPiExtensionStatus(completion: completion)
        }
    }

    private func relayPiExtensionRequest(
        eventName: String,
        perform: (DaemonClient, @escaping ([String: Any]?) -> Void) -> Void
    ) {
        guard let daemonClient else {
            evaluateJavaScriptEvent(name: eventName, detail: Self.piExtensionDetail(nil))
            return
        }
        perform(daemonClient) { [weak self] payload in
            self?.evaluateJavaScriptEvent(name: eventName, detail: Self.piExtensionDetail(payload))
        }
    }

    private static func piExtensionDetail(_ payload: [String: Any]?) -> [String: Any] {
        guard let payload else {
            return [
                "ok": false,
                "message": "Pet daemon is not available",
                "available": false,
                "installed": false,
                "needsUpdate": false,
            ]
        }
        return payload
    }

    private func installedPet(for payload: Any?) -> PetPackage? {
        guard let petID = Self.string(payload) else { return nil }
        return installedPetsProvider().first { $0.id == petID }
    }

    private func evaluateJavaScriptEvent(name: String, detail: [String: Any]) {
        let payload = (try? JSONSerialization.data(withJSONObject: detail, options: []))
            .flatMap { String(data: $0, encoding: .utf8) } ?? "{}"
        let script = """
        window.dispatchEvent(new CustomEvent("\(name)", { detail: \(payload) }));
        """
        webView.evaluateJavaScript(script, completionHandler: nil)
    }

    private static func browserIndexURL() -> URL? {
        var candidates: [URL] = []
        if let resourceURL = Bundle.main.resourceURL {
            candidates.append(
                resourceURL
                    .appendingPathComponent(Constants.browserResourceDirectory, isDirectory: true)
                    .appendingPathComponent("index.html")
            )
        }
        candidates.append(
            URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
                .appendingPathComponent("index.html")
        )
        return candidates.first { FileManager.default.fileExists(atPath: $0.path) }
    }

    static func installedPetPayload(_ pet: PetPackage) -> [String: Any] {
        [
            "source": "installed",
            "nativePetId": pet.id,
            "slug": pet.slug,
            "displayName": pet.displayName,
            "description": pet.detail,
            "kind": pet.kind,
            "submittedBy": pet.source.label,
            "tags": [],
            "spritesheetUrl": PetAssetSchemeHandler.assetURLString(for: pet),
            "frameWidth": pet.frameWidth,
            "frameHeight": pet.frameHeight,
            "canUninstall": pet.source == .app,
        ]
    }

    private static func shouldOpenExternally(_ url: URL) -> Bool {
        guard let scheme = url.scheme?.lowercased() else { return false }
        return scheme == "http" || scheme == "https"
    }

    private static func string(_ value: Any?) -> String? {
        guard let text = value as? String else { return nil }
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}
