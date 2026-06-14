import Cocoa

final class AppDelegate: NSObject, NSApplicationDelegate {
    private enum DefaultsKey {
        static let attentionMode = "CodexPets.attentionMode"
        static let bubbleMode = "CodexPets.bubbleMode"
        static let followsSystemReduceMotion = "CodexPets.followsSystemReduceMotion"
        static let alwaysReduceMotion = "CodexPets.alwaysReduceMotion"
        static let showsInFullScreen = "CodexPets.showsInFullScreen"
    }

    private let store = PetStore()
    private let overlay = PetOverlayController()
    private var server: StateServer?
    private var daemonProcess: DaemonProcessController?
    private var daemonClient: DaemonClient?
    private var statusItem: NSStatusItem?
    private var petdexBrowser: PetdexBrowserWindowController?
    private var guiSmokeRecorder: GUISmokeRecorder?
    private var updatePollTimer: Timer?
    private var updateStatus: UpdateStatus = .idle
    private var relaunchScheduled = false
    private var pets: [PetPackage] = []
    private var petRefIDs: [String] = []
    private var selectedPetID: String?
    private var defaultPetImportAttempted = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        guiSmokeRecorder = GUISmokeRecorder.fromEnvironment()
        guiSmokeRecorder?.recordLaunch()

        overlay.petBrowserRequestedHandler = { [weak self] in
            self?.openPetdexBrowser()
        }
        overlay.approvalDecisionHandler = { [weak self] approvalID, decision in
            self?.daemonClient?.respondToApproval(
                approvalID: approvalID,
                decision: decision,
                reason: decision == "approved" ? "Approved from pet overlay" : "Denied from pet overlay"
            )
        }
        overlay.updateRequestedHandler = { [weak self] in
            self?.handleUpdateAction()
        }
        overlay.interactionHandler = { [weak self] type, clicks in
            self?.daemonClient?.sendInteraction(type: type, clicks: clicks) { [weak self] murmur in
                self?.overlay.setBubble(murmur)
            }
        }
        overlay.muteRequestedHandler = { [weak self] seconds in
            self?.daemonClient?.muteMurmurs(seconds: seconds)
        }

        setupStatusItem()
        loadOverlaySettings()
        if StateServer.isDebugEnabled {
            server = StateServer(
                runtimeRoot: store.runtimeRoot,
                onState: { [weak self] state, duration in
                    self?.overlay.setState(state, duration: duration)
                },
                onBubble: { [weak self] text in
                self?.overlay.setBubble(text)
            },
            onEvent: { [weak self] type, label, importance in
                    self?.overlay.setEvent(type: type, label: label, importance: importance)
                }
            )
            server?.start()
        }

        let daemonSocketPath = DaemonClient.defaultSocketPath()
        daemonClient = DaemonClient(socketPath: daemonSocketPath)
        if let daemonBinary = DaemonProcessController.bundledDaemonURL() {
            let daemon = DaemonProcessController(
                binaryURL: daemonBinary,
                socketPath: daemonSocketPath,
                logURL: store.runtimeRoot.appendingPathComponent("pi-pet-daemon.log"),
                piExtensionSourceURL: DaemonProcessController.bundledPiExtensionSourceURL(),
                stateFileURL: store.appSupport.appendingPathComponent("daemon-state.json"),
                updateBuildScript: "macos/CodexPets/build.sh",
                petRoots: daemonPetRoots(),
                petImportRootURL: store.importedPetsRoot,
                catalogDirURL: Self.bundledCatalogDirectory(),
                dialogueHistoryURL: store.appSupport.appendingPathComponent("DialogueHistory.json")
            )
            daemon.onReady = { [weak self] in
                self?.daemonBecameReady()
            }
            daemonProcess = daemon
            daemon.start()
        } else {
            NSLog("CodexPets: bundled pi-pet-daemon not found; subscribing to existing socket")
            daemonBecameReady()
        }

        refreshPetsFromDaemon()
        rebuildMenu()

        if ProcessInfo.processInfo.environment["CODEX_PETS_GUI_SMOKE_OPEN_BROWSER"] == "1" {
            DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(300)) { [weak self] in
                self?.openPetdexBrowser()
            }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        updatePollTimer?.invalidate()
        daemonClient?.stop()
        daemonProcess?.stop()
    }

    /// Runs after the daemon socket becomes ready — both on first launch and
    /// after every automatic daemon restart.
    private func daemonBecameReady() {
        daemonClient?.startSnapshotSubscription { [weak self] snapshot in
            self?.applyDaemonSnapshot(snapshot)
        }
        refreshPetsFromDaemon()
        pushOverlaySettingsToDaemon()
        startUpdatePolling()
    }

    /// The daemon's brain murmurs on this host's behalf; keep its
    /// temperament in sync with the local menu settings.
    private func pushOverlaySettingsToDaemon() {
        daemonClient?.setOverlaySettings(
            attentionMode: overlay.attentionMode.rawValue,
            bubbleMode: overlay.bubbleMode.rawValue,
            reduceMotion: overlay.reduceMotion
        )
    }

    private func daemonPetRoots() -> [(source: String, url: URL)] {
        let home = FileManager.default.homeDirectoryForCurrentUser
        return [
            ("app", store.importedPetsRoot),
            ("petdex", home.appendingPathComponent(".petdex/pets", isDirectory: true)),
            ("codex", home.appendingPathComponent(".codex/pets", isDirectory: true)),
        ]
    }

    /// The bundled pet catalog shipped with the app; the daemon scans it and
    /// serves the merged picker rows (pets.browser.list).
    private static func bundledCatalogDirectory() -> URL? {
        var candidates: [URL] = []
        if let resourceURL = Bundle.main.resourceURL {
            candidates.append(
                resourceURL
                    .appendingPathComponent("PetdexBrowser", isDirectory: true)
                    .appendingPathComponent("prebundled-pets", isDirectory: true)
            )
        }
        candidates.append(
            URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
                .appendingPathComponent("prebundled-pets", isDirectory: true)
        )
        return candidates.first {
            FileManager.default.fileExists(atPath: $0.appendingPathComponent("pets", isDirectory: true).path)
        }
    }

    /// The update pipeline runs in the daemon; the app only drives the
    /// check cadence and restarts itself when an update has been built.
    private func startUpdatePolling() {
        updatePollTimer?.invalidate()
        daemonClient?.checkForUpdates()
        let timer = Timer(timeInterval: 5 * 60, repeats: true) { [weak self] _ in
            self?.daemonClient?.checkForUpdates()
        }
        RunLoop.main.add(timer, forMode: .common)
        updatePollTimer = timer
    }

    private func setupStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        item.button?.title = "CP"
        item.button?.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .bold)
        statusItem = item
    }

    private func rebuildMenu() {
        let menu = NSMenu()

        let title = NSMenuItem(title: "Codex Pets", action: nil, keyEquivalent: "")
        title.isEnabled = false
        menu.addItem(title)
        menu.addItem(.separator())

        let toggle = NSMenuItem(
            title: overlay.isVisible ? "Tuck Away Pet" : "Wake Pet",
            action: #selector(togglePet),
            keyEquivalent: ""
        )
        toggle.target = self
        toggle.isEnabled = selectedPetID != nil
        menu.addItem(toggle)

        let browsePetdex = NSMenuItem(title: "Browse Petdex...", action: #selector(openPetdexBrowser), keyEquivalent: "b")
        browsePetdex.target = self
        menu.addItem(browsePetdex)

        let importItem = NSMenuItem(title: "Import Pet Folder...", action: #selector(importPetFolder), keyEquivalent: "i")
        importItem.target = self
        menu.addItem(importItem)

        let refreshItem = NSMenuItem(title: "Refresh Installed Pets", action: #selector(refreshPets), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)

        menu.addItem(petsSubmenu())
        menu.addItem(statesSubmenu())
        menu.addItem(sizeSubmenu())
        menu.addItem(settingsSubmenu())

        menu.addItem(.separator())

        let hello = NSMenuItem(title: "Show Test Bubble", action: #selector(showTestBubble), keyEquivalent: "")
        hello.target = self
        hello.isEnabled = selectedPetID != nil
        menu.addItem(hello)

        if server != nil {
            let curl = NSMenuItem(title: "Copy Debug State API Curl", action: #selector(copyStateCurl), keyEquivalent: "")
            curl.target = self
            menu.addItem(curl)
        }

        let storage = NSMenuItem(title: "Open Storage Folder", action: #selector(openStorageFolder), keyEquivalent: "")
        storage.target = self
        menu.addItem(storage)

        let checkUpdate = NSMenuItem(title: "Check for Updates", action: #selector(checkForUpdatesFromMenu), keyEquivalent: "u")
        checkUpdate.target = self
        menu.addItem(checkUpdate)

        menu.addItem(.separator())

        let quit = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)

        statusItem?.menu = menu
    }

    private func petsSubmenu() -> NSMenuItem {
        let item = NSMenuItem(title: "Pets", action: nil, keyEquivalent: "")
        let submenu = NSMenu()
        if pets.isEmpty {
            let empty = NSMenuItem(title: "No pets found", action: nil, keyEquivalent: "")
            empty.isEnabled = false
            submenu.addItem(empty)
        } else {
            for pet in pets {
                let menuItem = NSMenuItem(
                    title: "\(pet.displayName) (\(pet.source.label))",
                    action: #selector(selectPetFromMenu(_:)),
                    keyEquivalent: ""
                )
                menuItem.target = self
                menuItem.representedObject = pet.id
                menuItem.state = pet.id == selectedPetID ? .on : .off
                submenu.addItem(menuItem)
            }
        }
        item.submenu = submenu
        return item
    }

    private func statesSubmenu() -> NSMenuItem {
        let item = NSMenuItem(title: "State", action: nil, keyEquivalent: "")
        let submenu = NSMenu()
        for state in PetAnimationState.defaults {
            let stateItem = NSMenuItem(title: state.label, action: #selector(selectState(_:)), keyEquivalent: "")
            stateItem.target = self
            stateItem.representedObject = state.id
            stateItem.state = state.id == overlay.currentStateID ? .on : .off
            stateItem.isEnabled = selectedPetID != nil
            submenu.addItem(stateItem)
        }
        item.submenu = submenu
        return item
    }

    private func sizeSubmenu() -> NSMenuItem {
        let item = NSMenuItem(title: "Size", action: nil, keyEquivalent: "")
        let submenu = NSMenu()
        let sizes: [(String, CGFloat)] = [
            ("Small", 0.58),
            ("Normal", 0.76),
            ("Large", 1.0),
            ("Huge", 1.24),
        ]
        for (label, value) in sizes {
            let sizeItem = NSMenuItem(title: label, action: #selector(selectSize(_:)), keyEquivalent: "")
            sizeItem.target = self
            sizeItem.representedObject = value
            sizeItem.state = abs(overlay.scale - value) < 0.01 ? .on : .off
            submenu.addItem(sizeItem)
        }
        item.submenu = submenu
        return item
    }

    private func settingsSubmenu() -> NSMenuItem {
        let item = NSMenuItem(title: "Settings", action: nil, keyEquivalent: "")
        let submenu = NSMenu()

        let animation = NSMenuItem(title: "Animation", action: nil, keyEquivalent: "")
        let animationSubmenu = NSMenu()
        for mode in PetAttentionMode.allCases {
            let modeItem = NSMenuItem(title: mode.label, action: #selector(selectAttentionMode(_:)), keyEquivalent: "")
            modeItem.target = self
            modeItem.representedObject = mode.rawValue
            modeItem.state = overlay.attentionMode == mode ? .on : .off
            animationSubmenu.addItem(modeItem)
        }
        animation.submenu = animationSubmenu
        submenu.addItem(animation)

        let bubbles = NSMenuItem(title: "Bubbles", action: nil, keyEquivalent: "")
        let bubblesSubmenu = NSMenu()
        for mode in PetBubbleMode.allCases {
            let bubbleItem = NSMenuItem(title: mode.label, action: #selector(selectBubbleMode(_:)), keyEquivalent: "")
            bubbleItem.target = self
            bubbleItem.representedObject = mode.rawValue
            bubbleItem.state = overlay.bubbleMode == mode ? .on : .off
            bubblesSubmenu.addItem(bubbleItem)
        }
        bubbles.submenu = bubblesSubmenu
        submenu.addItem(bubbles)

        let muteToday = NSMenuItem(title: "Mute Murmurs Today", action: #selector(muteMurmursToday), keyEquivalent: "")
        muteToday.target = self
        submenu.addItem(muteToday)

        submenu.addItem(.separator())

        let followReduce = NSMenuItem(
            title: "Reduced Motion: Follow System",
            action: #selector(toggleFollowSystemReduceMotion),
            keyEquivalent: ""
        )
        followReduce.target = self
        followReduce.state = overlay.followsSystemReduceMotion ? .on : .off
        submenu.addItem(followReduce)

        let alwaysReduce = NSMenuItem(
            title: "Reduced Motion: Always Reduce",
            action: #selector(toggleAlwaysReduceMotion),
            keyEquivalent: ""
        )
        alwaysReduce.target = self
        alwaysReduce.state = overlay.alwaysReduceMotion ? .on : .off
        submenu.addItem(alwaysReduce)

        submenu.addItem(.separator())

        let fullScreen = NSMenuItem(
            title: "Show in Full-Screen Apps",
            action: #selector(toggleShowInFullScreen),
            keyEquivalent: ""
        )
        fullScreen.target = self
        fullScreen.state = overlay.showsInFullScreen ? .on : .off
        submenu.addItem(fullScreen)

        let mouseMode = NSMenuItem(title: "Mouse Reactions: Near Pet Only", action: nil, keyEquivalent: "")
        mouseMode.isEnabled = false
        submenu.addItem(mouseMode)

        let permissionMode = NSMenuItem(title: "Anywhere Reactions Require Input Monitoring", action: nil, keyEquivalent: "")
        permissionMode.isEnabled = false
        submenu.addItem(permissionMode)

        item.submenu = submenu
        return item
    }

    private func selectPet(_ pet: PetPackage, publish: Bool = true) {
        selectedPetID = pet.id
        overlay.setPet(pet)
        overlay.setBubble("")
        if publish {
            daemonClient?.selectPet(pet.id)
        }
        rebuildMenu()
    }

    @objc private func togglePet() {
        overlay.toggle()
        rebuildMenu()
    }

    @objc private func openPetdexBrowser() {
        if petdexBrowser == nil {
            petdexBrowser = makePetdexBrowser()
        }
        petdexBrowser?.prepareForDisplay()
        NSApp.activate(ignoringOtherApps: true)
        petdexBrowser?.showWindow(nil)
        petdexBrowser?.window?.makeKeyAndOrderFront(nil)
    }

    private func makePetdexBrowser() -> PetdexBrowserWindowController {
        let controller = PetdexBrowserWindowController(
            daemonClient: daemonClient,
            installedPetsProvider: { [weak self] in
                self?.pets ?? []
            },
            onImport: { [weak self] displayName in
                guard let self else { return }
                self.refreshPetsFromDaemon()
                self.overlay.setBubble("Imported \(displayName)")
            },
            onSelectInstalled: { [weak self] pet in
                guard let self else { return }
                self.selectPet(pet)
            },
            onBrowserLoaded: { [weak self] payload in
                self?.guiSmokeRecorder?.recordPetdexBrowser(payload)
            },
            onClose: { [weak self] in
                self?.petdexBrowser = nil
            }
        )
        return controller
    }

    @objc private func importPetFolder() {
        NSApp.activate(ignoringOtherApps: true)
        let panel = NSOpenPanel()
        panel.title = "Import Codex Pet Folder"
        panel.message = "Choose a folder containing pet.json and spritesheet.webp or spritesheet.png."
        panel.prompt = "Import"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false

        guard panel.runModal() == .OK, let url = panel.url else {
            rebuildMenu()
            return
        }

        importPetFolderThroughDaemon(url)
        rebuildMenu()
    }

    @objc private func refreshPets() {
        refreshPetsFromDaemon()
    }

    @objc private func selectPetFromMenu(_ sender: NSMenuItem) {
        guard
            let id = sender.representedObject as? String,
            let pet = pets.first(where: { $0.id == id })
        else {
            return
        }
        selectPet(pet)
    }

    @objc private func selectState(_ sender: NSMenuItem) {
        guard let id = sender.representedObject as? String else { return }
        overlay.setState(id)
        rebuildMenu()
    }

    @objc private func selectSize(_ sender: NSMenuItem) {
        guard let size = sender.representedObject as? CGFloat else { return }
        overlay.setScale(size)
        rebuildMenu()
    }

    @objc private func selectAttentionMode(_ sender: NSMenuItem) {
        guard
            let raw = sender.representedObject as? String,
            let mode = PetAttentionMode(rawValue: raw)
        else { return }
        overlay.setAttentionMode(mode)
        UserDefaults.standard.set(mode.rawValue, forKey: DefaultsKey.attentionMode)
        pushOverlaySettingsToDaemon()
        rebuildMenu()
    }

    @objc private func selectBubbleMode(_ sender: NSMenuItem) {
        guard
            let raw = sender.representedObject as? String,
            let mode = PetBubbleMode(rawValue: raw)
        else { return }
        overlay.setBubbleMode(mode)
        UserDefaults.standard.set(mode.rawValue, forKey: DefaultsKey.bubbleMode)
        pushOverlaySettingsToDaemon()
        rebuildMenu()
    }

    @objc private func muteMurmursToday() {
        overlay.muteMurmursForToday()
        rebuildMenu()
    }

    @objc private func toggleFollowSystemReduceMotion() {
        let next = !overlay.followsSystemReduceMotion
        overlay.setFollowsSystemReduceMotion(next)
        UserDefaults.standard.set(next, forKey: DefaultsKey.followsSystemReduceMotion)
        pushOverlaySettingsToDaemon()
        rebuildMenu()
    }

    @objc private func toggleAlwaysReduceMotion() {
        let next = !overlay.alwaysReduceMotion
        overlay.setAlwaysReduceMotion(next)
        UserDefaults.standard.set(next, forKey: DefaultsKey.alwaysReduceMotion)
        pushOverlaySettingsToDaemon()
        rebuildMenu()
    }

    @objc private func toggleShowInFullScreen() {
        let next = !overlay.showsInFullScreen
        overlay.setShowsInFullScreen(next)
        UserDefaults.standard.set(next, forKey: DefaultsKey.showsInFullScreen)
        rebuildMenu()
    }

    @objc private func showTestBubble() {
        overlay.setBubble("Codex Pets is awake")
    }

    @objc private func copyStateCurl() {
        guard let snippet = server?.copyCurlSnippet() else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(snippet, forType: .string)
    }

    @objc private func openStorageFolder() {
        NSWorkspace.shared.open(store.appSupport)
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    private func handleUpdateAction() {
        switch updateStatus {
        case .available:
            daemonClient?.applyUpdate()
        case .failed:
            daemonClient?.dismissUpdateFailure()
            daemonClient?.checkForUpdates()
        default:
            break
        }
    }

    @objc private func checkForUpdatesFromMenu() {
        switch updateStatus {
        case .idle:
            daemonClient?.checkForUpdates()
        case .failed:
            daemonClient?.dismissUpdateFailure()
            daemonClient?.checkForUpdates()
        case .available:
            daemonClient?.applyUpdate()
        default:
            break
        }
    }

    private func importPetFolderThroughDaemon(_ url: URL, quietly: Bool = false) {
        guard let daemonClient else {
            if !quietly { showErrorMessage("Pet daemon is not available") }
            return
        }
        // The daemon validates, canonicalizes, and copies the package.
        daemonClient.importPet(directory: url.path) { [weak self] payload in
            guard let self else { return }
            if payload?["ok"] as? Bool == true {
                self.refreshPetsFromDaemon()
                if !quietly, let pet = payload?["pet"] as? [String: Any] {
                    self.overlay.setBubble("Imported \(pet["displayName"] as? String ?? "pet")")
                }
                return
            }
            let message = payload?["message"] as? String ?? "Pet import failed"
            if quietly {
                NSLog("CodexPets: default pet import failed: %@", message)
            } else {
                self.showErrorMessage(message)
            }
        }
    }

    private func applyUpdateState(_ state: DaemonUpdateState?) {
        let status = UpdateStatus.fromDaemon(state)
        guard status != updateStatus else { return }
        updateStatus = status
        overlay.setUpdateStatus(status)

        // The daemon built the new version; restart into it exactly once.
        if case .restarting = status, !relaunchScheduled {
            relaunchScheduled = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                AppRelauncher.relaunch()
            }
        }
    }

    private func showError(_ error: Error) {
        let alert = NSAlert()
        alert.messageText = "Could not import pet"
        alert.informativeText = error.localizedDescription
        alert.alertStyle = .warning
        alert.runModal()
    }

    private func showErrorMessage(_ message: String) {
        let alert = NSAlert()
        alert.messageText = "Could not import pet"
        alert.informativeText = message
        alert.alertStyle = .warning
        alert.runModal()
    }

    private func applyDaemonSnapshot(_ snapshot: DaemonSnapshot) {
        // Snapshots arrive on every session event; only reload pet packages
        // from disk when the installed list actually changed.
        let refIDs = snapshot.installedPets.map(\.id)
        if refIDs != petRefIDs {
            petRefIDs = refIDs
            pets = snapshot.installedPets.compactMap(petPackage)
            petdexBrowser?.refreshInstalledPets()
            rebuildMenu()
        }
        if let remoteID = snapshot.selectedPetId, !remoteID.isEmpty {
            if remoteID != selectedPetID, let selected = pets.first(where: { $0.id == remoteID }) {
                selectPet(selected, publish: false)
            }
        } else if let selectedPetID, pets.contains(where: { $0.id == selectedPetID }) {
            // Fresh daemon state (first run): seed it with the provisional
            // selection so extension profiles and other overlays see a pet.
            daemonClient?.selectPet(selectedPetID)
        } else if let first = pets.first {
            selectPet(first)
        } else {
            selectedPetID = nil
            overlay.setPet(nil)
            overlay.hide()
            rebuildMenu()
            importDefaultPetIfNeeded()
        }
        overlay.applyDaemonSnapshot(snapshot)
        applyUpdateState(snapshot.update)
        guiSmokeRecorder?.recordSnapshot(snapshot, overlayState: overlay.currentStateID)
    }

    /// First run on a fresh machine: nothing is installed anywhere, so seed
    /// the daemon with the bundled default pet through the regular import.
    private func importDefaultPetIfNeeded() {
        guard !defaultPetImportAttempted else { return }
        defaultPetImportAttempted = true
        guard let bundled = store.bundledDefaultPetDirectory(slug: "boba") else { return }
        importPetFolderThroughDaemon(bundled, quietly: true)
    }

    private func refreshPetsFromDaemon() {
        daemonClient?.refreshPets()
    }

    private func loadOverlaySettings() {
        let defaults = UserDefaults.standard
        let attentionMode = PetAttentionMode(rawValue: defaults.string(forKey: DefaultsKey.attentionMode) ?? "") ?? .default
        let bubbleMode = PetBubbleMode(rawValue: defaults.string(forKey: DefaultsKey.bubbleMode) ?? "") ?? .all
        let followsSystemReduceMotion = defaults.object(forKey: DefaultsKey.followsSystemReduceMotion) as? Bool ?? true
        let alwaysReduceMotion = defaults.bool(forKey: DefaultsKey.alwaysReduceMotion)
        let showsInFullScreen = defaults.bool(forKey: DefaultsKey.showsInFullScreen)

        overlay.applySettings(
            attentionMode: attentionMode,
            bubbleMode: bubbleMode,
            followsSystemReduceMotion: followsSystemReduceMotion,
            alwaysReduceMotion: alwaysReduceMotion,
            showsInFullScreen: showsInFullScreen
        )
    }
}

final class GUISmokeRecorder {
    private let url: URL
    private let queue = DispatchQueue(label: "CodexPets.GUISmokeRecorder")
    private let dateFormatter = ISO8601DateFormatter()

    private init(url: URL) {
        self.url = url
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: url.path, contents: nil)
    }

    static func fromEnvironment() -> GUISmokeRecorder? {
        guard
            let path = ProcessInfo.processInfo.environment["CODEX_PETS_GUI_SMOKE_FILE"]?
                .trimmingCharacters(in: .whitespacesAndNewlines),
            !path.isEmpty
        else {
            return nil
        }
        return GUISmokeRecorder(url: URL(fileURLWithPath: path))
    }

    func recordLaunch() {
        write([
            "event": "launch",
            "pid": ProcessInfo.processInfo.processIdentifier,
        ])
    }

    func recordSnapshot(_ snapshot: DaemonSnapshot, overlayState: String) {
        let presentation = snapshot.presentationOrIdle
        write([
            "event": "snapshot",
            "attention": snapshot.attention,
            "stateID": presentation.stateId,
            "overlayState": overlayState,
            "bubble": presentation.bubble ?? "",
            "sessions": snapshot.sessions.count,
            "pendingApprovals": snapshot.pendingApprovals.count,
            "installedPets": snapshot.installedPets.count,
            "selectedPetId": snapshot.selectedPetId ?? "",
        ])
    }

    func recordPetdexBrowser(_ payload: [String: Any]) {
        write(payload)
    }

    private func write(_ fields: [String: Any]) {
        queue.async { [url, dateFormatter] in
            var object = fields
            object["timestamp"] = dateFormatter.string(from: Date())
            guard
                JSONSerialization.isValidJSONObject(object),
                var data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
            else {
                return
            }
            data.append(0x0a)
            guard let handle = try? FileHandle(forWritingTo: url) else { return }
            defer { try? handle.close() }
            _ = try? handle.seekToEnd()
            _ = try? handle.write(contentsOf: data)
        }
    }
}
