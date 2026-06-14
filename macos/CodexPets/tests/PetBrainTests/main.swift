import Cocoa
import Darwin
import Foundation

private final class TestClock {
    var now: TimeInterval

    init(_ now: TimeInterval = 0) {
        self.now = now
    }

    func advance(_ seconds: TimeInterval) {
        now += seconds
    }
}

private func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() {
        fputs("FAIL: \(message)\n", stderr)
        exit(1)
    }
}

private func expectDecision(_ decision: PetDecision?, _ message: String) -> PetDecision {
    guard let decision else {
        fputs("FAIL: expected decision — \(message)\n", stderr)
        exit(1)
    }
    return decision
}

private func runMainLoop(for seconds: TimeInterval) {
    let deadline = Date().addingTimeInterval(seconds)
    while Date() < deadline {
        RunLoop.current.run(mode: .default, before: min(Date().addingTimeInterval(0.02), deadline))
    }
}

private func testMouseProximityRequiresDwellAndCooldown() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })

    expect(brain.handle(.mouseNear(distance: 110)) == nil, "near cursor should dwell before curious")
    clock.advance(0.45)
    let curious = expectDecision(brain.handle(.mouseNear(distance: 100)), "near cursor after dwell")
    expect(curious.mood == .curious, "near cursor after dwell should be curious")
    expect(curious.stateID == "waiting", "curious should use waiting pose")

    clock.advance(1)
    expect(brain.handle(.mouseNear(distance: 90)) == nil, "curious should respect cooldown")
}

private func testFocusModeAndReduceMotionStayQuiet() {
    let clock = TestClock()
    let brain = PetBrain(mode: .focus, reduceMotion: true, now: { clock.now })

    clock.advance(1)
    expect(brain.handle(.mouseNear(distance: 80)) == nil, "focus mode should ignore passive mouse proximity")

    let running = expectDecision(brain.handle(.codexState("running")), "running still matters in focus mode")
    expect(running.mood == .focused, "running should map to focused mood")
    expect(running.stateID == "running", "running should keep running state id")
    expect(running.bubble == nil, "running should be quiet")
    expect(running.playback == .staticFrame(0), "reduce motion should force static playback")
}

private func testClickSpamBecomesAnnoyed() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })

    for _ in 0..<4 {
        _ = brain.handle(.clicked(count: 1))
        clock.advance(1)
    }
    let annoyed = expectDecision(brain.handle(.clicked(count: 1)), "spam click threshold")
    expect(annoyed.mood == .annoyed, "5 clicks in 10s should become annoyed")
    expect(annoyed.stateID == "failed", "annoyed should use failed/startled pose")
    expect(annoyed.bubble == nil, "murmur text lives in the daemon; local decisions stay silent")
}

private func testIdleAttentionBudgetCapsMicroIdle() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })
    var activeIdleCount = 0

    for _ in 0..<24 {
        if let decision = brain.handle(.idlePulse), decision.mood != .calm {
            activeIdleCount += 1
        }
        clock.advance(5)
    }

    expect(activeIdleCount <= 5, "default mode should cap micro-idle decisions to 5/min, got \(activeIdleCount)")
}

private func testLongRunningSettlesToWaitingPose() {
    let clock = TestClock()
    let brain = PetBrain(mode: .playful, now: { clock.now })

    _ = brain.handle(.codexState("running"))
    clock.advance(6 * 60)
    let settled = expectDecision(brain.handle(.idlePulse), "long-running task should settle")
    expect(settled.mood == .focused, "long-running task should stay focused")
    expect(settled.stateID == "waiting", "long-running task should stop endless running")
    expect(settled.playback == .staticFrame(0), "long-running task should be static")
}

private func testManualAnimatedStatesPlayOnce() {
    let brain = PetBrain(mode: .default)

    let wave = expectDecision(brain.handle(.codexState("waving")), "manual waving state")
    expect(wave.stateID == "waving", "manual waving should keep state")
    expect(wave.playback == .playOnce, "manual waving should play once")

    let jump = expectDecision(brain.handle(.codexState("jumping")), "manual jumping state")
    expect(jump.stateID == "jumping", "manual jumping should keep state")
    expect(jump.playback == .playOnce, "manual jumping should play once")
}

private func testDoubleClickOverridesSingleClickCooldown() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })

    _ = brain.handle(.clicked(count: 1))
    clock.advance(0.2)
    let doubleClick = expectDecision(brain.handle(.clicked(count: 2)), "double-click should not be swallowed by happy cooldown")
    expect(doubleClick.mood == .happy, "double-click should be happy")
    expect(doubleClick.stateID == "jumping", "double-click should use petting/jumping pose")
}

private func testDragUsesDirectionalRunningLoop() {
    let brain = PetBrain(mode: .default)

    let right = expectDecision(brain.handle(.dragged(direction: .right)), "dragging right")
    expect(right.mood == .happy, "dragging should stay an interaction")
    expect(right.stateID == "running-right", "dragging right should use right run animation")
    expect(right.playback == .loop, "dragging should actively loop until mouse-up")
    expect(right.duration == nil, "dragging should be ended by mouse-up instead of a fixed duration")

    let left = expectDecision(brain.handle(.dragged(direction: .left)), "dragging left")
    expect(left.stateID == "running-left", "dragging left should use left run animation")
    expect(left.playback == .loop, "dragging left should actively loop until mouse-up")
}

private func testSuccessEventStillAppliesDuringHappyCooldown() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })

    _ = brain.handle(.clicked(count: 1))
    _ = brain.handle(.codexState("running"))
    clock.advance(1)
    let success = expectDecision(
        brain.handle(.codexEvent(type: "task.succeeded", label: "Tests passed", importance: .low)),
        "success should not be dropped during happy cooldown"
    )
    expect(success.mood == .happy, "success should remain happy")
    expect(success.stateID == "waving", "success should visibly complete")
}

private func testReduceMotionOffResumesRunningPlayback() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, reduceMotion: true, now: { clock.now })

    let reduced = expectDecision(brain.handle(.codexState("running")), "running in reduce motion")
    expect(reduced.playback == .staticFrame(0), "running should be static while reduce motion is on")

    let resumed = expectDecision(brain.handle(.reduceMotionChanged(false)), "turning reduce motion off")
    expect(resumed.mood == .focused, "running should still be focused")
    expect(resumed.stateID == "running", "running state should be preserved")
    if case .loopWithPause = resumed.playback {
        // expected
    } else {
        fputs("FAIL: running should resume loopWithPause after reduce motion is disabled\n", stderr)
        exit(1)
    }
}

private func testMouseLeavingRadiusResetsDwell() {
    let clock = TestClock()
    let brain = PetBrain(mode: .default, now: { clock.now })

    expect(brain.handle(.mouseNear(distance: 100)) == nil, "initial near starts dwell")
    clock.advance(0.45)
    expect(brain.handle(.mouseNear(distance: 300)) == nil, "leaving radius should reset dwell")
    clock.advance(0.1)
    expect(brain.handle(.mouseNear(distance: 100)) == nil, "re-enter should require a fresh dwell")
}

private func makeTestPetPackage() -> PetPackage {
    PetPackage(
        slug: "test",
        displayName: "Test Pet",
        detail: "Test",
        kind: "pet",
        source: .app,
        directory: URL(fileURLWithPath: "/tmp"),
        spritesheet: URL(fileURLWithPath: "/tmp/missing-spritesheet.png"),
        frameWidth: 192,
        frameHeight: 208,
        states: PetAnimationState.defaults
    )
}

private func testUpdateStatusMapsWireStates() {
    func state(available: Bool? = nil, behind: Int? = nil, stage: String? = nil, message: String? = nil) -> DaemonUpdateState {
        DaemonUpdateState(available: available, commitsBehind: behind, stage: stage, message: message)
    }

    expect(UpdateStatus.fromDaemon(nil) == .idle, "missing update state should map to idle")
    expect(UpdateStatus.fromDaemon(state()) == .idle, "empty update state should map to idle")
    expect(UpdateStatus.fromDaemon(state(stage: "checking")) == .checking, "checking stage should map to checking")
    expect(UpdateStatus.fromDaemon(state(available: true, behind: 3)) == .available(commitsBehind: 3), "available state should keep commit count")
    expect(UpdateStatus.fromDaemon(state(available: true)) == .available(commitsBehind: 0), "available without count should map to rebuild")
    expect(UpdateStatus.fromDaemon(state(stage: "pulling")) == .updating(stage: "Pulling…"), "pulling stage should map to updating")
    expect(UpdateStatus.fromDaemon(state(stage: "building")) == .updating(stage: "Building…"), "building stage should map to updating")
    expect(UpdateStatus.fromDaemon(state(stage: "failed", message: "git pull failed")) == .failed(message: "git pull failed"), "failed stage should carry its message")
    expect(UpdateStatus.fromDaemon(state(stage: "restartPending")) == .restarting, "restartPending should map to restarting")
}

private func testOverlayHitTestingMakesWholePetBodyDraggable() {
    let view = PetOverlayView(frame: NSRect(x: 0, y: 0, width: 190, height: 235))
    let pet = makeTestPetPackage()
    view.setPet(pet, state: PetAnimationState.defaults[0], scale: 0.76, playback: .staticFrame(0))
    view.bubbleText = "пора посмотреть."
    view.bubbleActionHandler = {}

    let bubble = view.bubbleRect
    let sprite = view.spriteRect
    let bubblePoint = NSPoint(x: bubble.midX, y: bubble.midY)
    let spritePoint = NSPoint(x: sprite.midX, y: sprite.midY)
    let spriteCornerPoint = NSPoint(x: sprite.minX + 1, y: sprite.minY + 1)
    let bodyEdgePoint = NSPoint(x: view.petBodyHitRect.minX + 1, y: sprite.midY)
    let gapPoint = NSPoint(x: sprite.midX, y: (bubble.maxY + sprite.minY) / 2)
    let lowerBodyPoint = NSPoint(x: view.bounds.midX, y: view.bounds.maxY - 2)

    expect(view.containsInteractivePoint(bubblePoint), "bubble should be clickable")
    expect(view.containsInteractivePoint(spritePoint), "sprite should stay clickable")
    expect(view.containsInteractivePoint(spriteCornerPoint), "whole sprite body should be draggable")
    expect(view.containsInteractivePoint(bodyEdgePoint), "pet body hitbox should be draggable")
    expect(view.containsInteractivePoint(gapPoint), "space around the pet should drag with the body")
    expect(view.containsInteractivePoint(lowerBodyPoint), "lower pet overlay should be draggable")

    view.pendingApprovalID = "approval-1"
    let approvalBubble = view.bubbleRect
    let approvePoint = NSPoint(x: approvalBubble.minX + 35, y: approvalBubble.maxY - 17)
    let denyPoint = NSPoint(x: approvalBubble.maxX - 35, y: approvalBubble.maxY - 17)
    expect(view.approvalDecision(at: approvePoint)?.decision == "approved", "approval bubble approve button should be hit-testable")
    expect(view.approvalDecision(at: denyPoint)?.decision == "denied", "approval bubble deny button should be hit-testable")
}

private func testOverlayRightClickRequestsPetBrowser() {
    let view = PetOverlayView(frame: NSRect(x: 0, y: 0, width: 190, height: 235))
    let pet = makeTestPetPackage()
    view.setPet(pet, state: PetAnimationState.defaults[0], scale: 0.76, playback: .staticFrame(0))

    var rightClicks = 0
    view.rightClickHandler = {
        rightClicks += 1
    }
    view.handleRightClick(at: NSPoint(x: view.petBodyHitRect.midX, y: view.petBodyHitRect.midY), activateApp: false)
    expect(rightClicks == 1, "right-clicking the pet body should request the pet browser")

    view.bubbleText = "hello"
    view.bubbleActionHandler = {}
    view.handleRightClick(at: NSPoint(x: view.bubbleRect.midX, y: view.bubbleRect.midY), activateApp: false)
    expect(rightClicks == 1, "right-clicking a murmur bubble should not open the pet browser")
}

private func testOverlayKeepsBubbleThroughAutoIdleReset() {
    let overlay = PetOverlayController()
    overlay.setPet(makeTestPetPackage())
    overlay.hide()

    overlay.setState("waving", duration: 0.08)
    overlay.setBubble("agent done", autoClearAfter: 0.3)

    runMainLoop(for: 0.14)
    expect(overlay.currentStateID == "idle", "short completion pose should auto-reset to idle")
    expect(overlay.currentBubbleText == "agent done", "auto idle reset should not clear the active bubble")

    runMainLoop(for: 0.25)
    expect(overlay.currentBubbleText.isEmpty, "bubble should still clear on its own timer")
    overlay.hide()
}

private func testPetPackageMapsDaemonRefWithoutManifestParsing() {
    let ref = DaemonPetRef(
        id: "app:boba:/pets/boba",
        slug: "boba",
        displayName: "Boba",
        description: "A bundled pet.",
        kind: "creature",
        source: "app",
        path: "/pets/boba",
        spritesheetPath: "spritesheet.webp",
        frameWidth: 96,
        frameHeight: 104,
        license: "MIT",
        attribution: "tests"
    )

    let pet = petPackage(from: ref)
    expect(pet != nil, "enriched daemon ref should map to a render package")
    expect(pet?.id == ref.id, "mapped package id must round-trip the daemon pet id")
    expect(pet?.displayName == "Boba", "mapped package should carry display name")
    expect(pet?.detail == "A bundled pet.", "mapped package should carry description")
    expect(pet?.kind == "creature", "mapped package should carry kind")
    expect(pet?.frameWidth == 96 && pet?.frameHeight == 104, "mapped package should carry frame size")
    expect(pet?.spritesheet.path == "/pets/boba/spritesheet.webp", "mapped package should resolve the spritesheet inside the package directory")

    let incomplete = DaemonPetRef(
        id: "app:x:/pets/x", slug: nil, displayName: "X", description: nil, kind: nil,
        source: "app", path: "/pets/x", spritesheetPath: nil, frameWidth: nil, frameHeight: nil,
        license: nil, attribution: nil
    )
    expect(petPackage(from: incomplete) == nil, "refs without slug or spritesheet cannot render and are skipped")
}

private func testDaemonSnapshotDecodesWirePresentation() {
    let wire = """
    {
      "attention": "failed",
      "sessions": [{"id": "s1", "status": "failed", "safeSummary": "provider HTTP 500", "startedAt": "2026-06-12T10:00:00Z", "updatedAt": "2026-06-12T10:00:01Z"}],
      "pendingApprovals": [],
      "installedPets": [],
      "catalogs": {},
      "presentation": {"stateId": "failed", "bubble": "Pi failed: provider HTTP 500", "autoClearSeconds": 8, "activeSessionIds": []},
      "updatedAt": "2026-06-12T10:00:01Z"
    }
    """
    let snapshot = try? JSONDecoder().decode(DaemonSnapshot.self, from: Data(wire.utf8))
    expect(snapshot != nil, "snapshot with wire presentation should decode")
    expect(snapshot?.presentationOrIdle.stateId == "failed", "wire presentation state should decode")
    expect(snapshot?.presentationOrIdle.bubble == "Pi failed: provider HTTP 500", "wire presentation bubble should decode")
    expect(snapshot?.presentationOrIdle.autoClearAfter == 8, "wire presentation auto-clear should decode")

    let withoutPresentation = """
    {"attention": "idle", "sessions": [], "pendingApprovals": [], "installedPets": []}
    """
    let fallback = try? JSONDecoder().decode(DaemonSnapshot.self, from: Data(withoutPresentation.utf8))
    expect(fallback?.presentationOrIdle.stateId == "idle", "snapshot without presentation should fall back to idle")
    expect(fallback?.presentationOrIdle.autoClearAfter == nil, "fallback presentation should not auto-clear")
}

private func testOverlayKeepsDaemonDoneBubbleAcrossIdleSnapshot() {
    let overlay = PetOverlayController()
    overlay.setPet(makeTestPetPackage())
    overlay.hide()

    let done = DaemonSnapshot(
        attention: "done",
        sessions: [
            DaemonSession(
                id: "s1",
                cwd: nil,
                title: nil,
                status: "done",
                safeSummary: "agent done",
                tools: nil
            ),
        ],
        pendingApprovals: [],
        selectedPetId: nil,
        installedPets: [],
        presentation: DaemonPresentation(
            stateId: "waving",
            bubble: "Pi done: agent done",
            autoClearSeconds: 6,
            activeSessionIds: []
        ),
        update: nil
    )
    let idle = DaemonSnapshot(
        attention: "idle",
        sessions: [],
        pendingApprovals: [],
        selectedPetId: nil,
        installedPets: [],
        presentation: DaemonPresentation(
            stateId: "idle",
            bubble: nil,
            autoClearSeconds: nil,
            activeSessionIds: []
        ),
        update: nil
    )

    overlay.applyDaemonSnapshot(done)
    expect(overlay.currentBubbleText == "Pi done: agent done", "done snapshot should show the completion bubble")

    overlay.applyDaemonSnapshot(idle)
    expect(overlay.currentStateID == "idle", "idle snapshot should still move the pet to idle")
    expect(overlay.currentBubbleText == "Pi done: agent done", "idle snapshot should not immediately clear an auto-clearing done bubble")

    overlay.setBubble("stale running", autoClearAfter: nil)
    overlay.applyDaemonSnapshot(idle)
    expect(overlay.currentBubbleText.isEmpty, "idle snapshot should clear non-auto-clearing daemon bubbles")
    overlay.hide()
}

private func testStateServerRequiresExplicitDebugFlag() {
    unsetenv("CODEX_PETS_ENABLE_HTTP_STATE_API")
    expect(!StateServer.isDebugEnabled, "legacy HTTP state API should be disabled by default")

    setenv("CODEX_PETS_ENABLE_HTTP_STATE_API", "1", 1)
    expect(StateServer.isDebugEnabled, "legacy HTTP state API should accept explicit debug flag")

    setenv("CODEX_PETS_ENABLE_HTTP_STATE_API", "false", 1)
    expect(!StateServer.isDebugEnabled, "legacy HTTP state API should reject false-like flag")
    unsetenv("CODEX_PETS_ENABLE_HTTP_STATE_API")
}

private func testPetdexBrowserBridgeActionAllowlist() {
    let allowed = Set(PetdexBrowserBridgeAction.allCases.map(\.rawValue))
    expect(
        allowed == Set([
            "importPet",
            "listBrowserPets",
            "listInstalledPets",
            "selectInstalledPet",
            "installPiExtension",
            "uninstallPiExtension",
            "getPiExtensionStatus",
        ]),
        "native bridge should expose only the expected allowlisted actions"
    )
    expect(PetdexBrowserBridgeAction(rawValue: "openShell") == nil, "native bridge should reject unknown actions")
    expect(PetdexBrowserBridgeAction(rawValue: "eval") == nil, "native bridge should reject privileged-looking actions")
}

private func testInstalledPetPayloadIsDataOnly() {
    let root = URL(fileURLWithPath: NSTemporaryDirectory())
        .appendingPathComponent("codex-pets-payload-\(UUID().uuidString)", isDirectory: true)
    let spriteURL = root.appendingPathComponent("spritesheet.png")
    try! FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    try! Data([0x89, 0x50, 0x4e, 0x47]).write(to: spriteURL)
    defer { try? FileManager.default.removeItem(at: root) }

    let pet = PetPackage(
        slug: "miso",
        displayName: "Miso",
        detail: "Imported pet",
        kind: "fox",
        source: .app,
        directory: root,
        spritesheet: spriteURL,
        frameWidth: 96,
        frameHeight: 104,
        states: PetAnimationState.defaults
    )

    let payload = PetdexBrowserWindowController.installedPetPayload(pet)
    expect(payload["source"] as? String == "installed", "installed pet payload should be marked as installed source")
    expect(payload["nativePetId"] as? String == pet.id, "installed pet payload should include opaque native pet id")
    let spritesheetUrl = payload["spritesheetUrl"] as? String ?? ""
    expect(spritesheetUrl.hasPrefix("codexpets-asset:///pet/"), "installed pet payload should reference the asset scheme instead of inlining bytes")
    expect(spritesheetUrl.hasSuffix("/spritesheet.png"), "asset URL should keep the spritesheet extension for MIME detection")
    expect(!spritesheetUrl.contains(root.path), "asset URL should not leak the raw filesystem path")
    let assetURL = URL(string: spritesheetUrl)
    expect(assetURL.flatMap(PetAssetSchemeHandler.petID(fromAssetURL:)) == pet.id, "asset URL should round-trip back to the pet id")
    expect(payload["canUninstall"] as? Bool == true, "app-imported installed pet should be uninstallable")
    expect(payload["frameWidth"] as? Int == 96, "installed pet payload should preserve frame width")
    expect(payload["frameHeight"] as? Int == 104, "installed pet payload should preserve frame height")

    let external = PetPackage(
        slug: "codex",
        displayName: "Codex Pet",
        detail: "Shared pet",
        kind: "pet",
        source: .codex,
        directory: URL(fileURLWithPath: "/Users/me/.codex/pets/codex"),
        spritesheet: URL(fileURLWithPath: "/Users/me/.codex/pets/codex/spritesheet.png"),
        frameWidth: 192,
        frameHeight: 208,
        states: PetAnimationState.defaults
    )
    let externalPayload = PetdexBrowserWindowController.installedPetPayload(external)
    expect(externalPayload["canUninstall"] as? Bool == false, "shared .codex installed pet should not be uninstallable from app storage")
}

private func testBundledGoDaemonServesPiProtocolOverUnixSocket() {
    guard
        let daemonBinaryPath = ProcessInfo.processInfo.environment["CODEX_PETS_TEST_DAEMON_BIN"],
        !daemonBinaryPath.isEmpty
    else {
        expect(false, "CODEX_PETS_TEST_DAEMON_BIN should point at a built pi-pet-daemon binary")
        return
    }
    let directory = URL(fileURLWithPath: "/tmp", isDirectory: true)
        .appendingPathComponent("codex-pets-go-daemon-\(UUID().uuidString)", isDirectory: true)
    let socketPath = directory.appendingPathComponent("pi-pet.sock").path
    let controller = DaemonProcessController(
        binaryURL: URL(fileURLWithPath: daemonBinaryPath),
        socketPath: socketPath,
        stateFileURL: directory.appendingPathComponent("daemon-state.json")
    )
    controller.start()
    defer {
        try? FileManager.default.removeItem(at: directory)
    }

    // The test runner has no main run loop, so wait for the socket directly
    // instead of relying on the controller's main-queue onReady callback.
    var socketReady = false
    let readyDeadline = Date().addingTimeInterval(10)
    while Date() < readyDeadline {
        if FileManager.default.fileExists(atPath: socketPath) {
            socketReady = true
            break
        }
        usleep(50_000)
    }
    expect(socketReady, "bundled Go daemon should create its Unix socket")

    let running = daemonRequest(
        socketPath: socketPath,
        method: "session.upsert",
        payload: [
            "sessionId": "swift-test",
            "cwd": "/tmp",
            "title": "Swift Test",
            "status": "running",
            "safeSummary": "agent running",
        ]
    )
    let runningPayload = running["payload"] as? [String: Any]
    expect(runningPayload?["attention"] as? String == "running", "daemon should derive running attention")
    expect((runningPayload?["sessions"] as? [[String: Any]])?.count == 1, "daemon should return sessions as an array")
    expect((runningPayload?["pendingApprovals"] as? [[String: Any]])?.isEmpty == true, "daemon should return approvals as an array")

    let toolUpdate = daemonRequest(
        socketPath: socketPath,
        method: "tool.update",
        payload: [
            "sessionId": "swift-test",
            "toolCallId": "tool-1",
            "toolName": "bash",
            "safeSummary": "bash running",
        ]
    )
    let toolPayload = toolUpdate["payload"] as? [String: Any]
    let sessions = toolPayload?["sessions"] as? [[String: Any]]
    let tools = sessions?.first?["tools"] as? [[String: Any]]
    expect(tools?.first?["state"] as? String == "running", "daemon should track tool update state")
    expect(tools?.first?["safeSummary"] as? String == "bash running", "daemon should track safe tool summaries")

    let removed = daemonRequest(
        socketPath: socketPath,
        method: "session.remove",
        payload: ["sessionId": "swift-test"]
    )
    let removedPayload = removed["payload"] as? [String: Any]
    expect(removedPayload?["attention"] as? String == "idle", "daemon should return to idle when a session is removed")
    expect((removedPayload?["sessions"] as? [[String: Any]])?.isEmpty == true, "daemon should remove terminated sessions")

    let lateTool = daemonRequest(
        socketPath: socketPath,
        method: "tool.start",
        payload: [
            "sessionId": "swift-test",
            "toolCallId": "late-tool",
            "toolName": "bash",
        ]
    )
    let lateToolPayload = lateTool["payload"] as? [String: Any]
    expect((lateToolPayload?["sessions"] as? [[String: Any]])?.isEmpty == true, "late tool events should not recreate removed sessions")

    _ = daemonRequest(
        socketPath: socketPath,
        method: "session.upsert",
        payload: [
            "sessionId": "approval-test",
            "status": "running",
        ]
    )
    final class ApprovalResultBox {
        var payload: [String: Any]?
    }
    let approvalResult = ApprovalResultBox()
    let approvalFinished = DispatchSemaphore(value: 0)
    DispatchQueue.global(qos: .utility).async {
        let response = daemonRequest(
            socketPath: socketPath,
            method: "approval.request",
            payload: [
                "approvalId": "approval-1",
                "sessionId": "approval-test",
                "toolName": "bash",
                "timeoutMillis": 5000,
            ]
        )
        approvalResult.payload = response["payload"] as? [String: Any]
        approvalFinished.signal()
    }

    var sawPendingApproval = false
    for _ in 0..<50 {
        let snapshotResponse = daemonRequest(socketPath: socketPath, method: "snapshot.get", payload: [:])
        let snapshotPayload = snapshotResponse["payload"] as? [String: Any]
        if (snapshotPayload?["pendingApprovals"] as? [[String: Any]])?.count == 1 {
            sawPendingApproval = true
            break
        }
        usleep(20_000)
    }
    expect(sawPendingApproval, "daemon should expose pending approval before session removal")

    let removedWithApproval = daemonRequest(
        socketPath: socketPath,
        method: "session.remove",
        payload: ["sessionId": "approval-test"]
    )
    let removedWithApprovalPayload = removedWithApproval["payload"] as? [String: Any]
    expect(removedWithApprovalPayload?["attention"] as? String == "idle", "session removal should clear approval attention")
    expect((removedWithApprovalPayload?["pendingApprovals"] as? [[String: Any]])?.isEmpty == true, "session removal should clear pending approvals")
    expect(approvalFinished.wait(timeout: .now() + .seconds(2)) == .success, "session removal should unblock pending approval requests")
    expect(approvalResult.payload?["decision"] as? String == "expired", "session removal should expire pending approval requests")
    expect(approvalResult.payload?["reason"] as? String == "session terminated", "session removal approval reason should explain termination")

    controller.stop()
    expect(!FileManager.default.fileExists(atPath: socketPath), "stopping the daemon should shut it down and remove its socket")
}

private func daemonRequest(socketPath: String, method: String, payload: [String: Any]) -> [String: Any] {
    let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
    expect(fd >= 0, "test socket should open")
    defer { Darwin.close(fd) }

    var timeout = timeval(tv_sec: 2, tv_usec: 0)
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
    expect(connectUnixForTest(fd, path: socketPath), "test socket should connect to daemon")

    let message: [String: Any] = [
        "version": 1,
        "kind": "request",
        "id": "test-\(method)",
        "method": method,
        "payload": payload,
    ]
    guard JSONSerialization.isValidJSONObject(message),
          var data = try? JSONSerialization.data(withJSONObject: message, options: [])
    else {
        expect(false, "test daemon request should encode")
        return [:]
    }
    data.append(0x0a)
    let sent = data.withUnsafeBytes { rawBuffer in
        Darwin.write(fd, rawBuffer.baseAddress, data.count)
    }
    expect(sent == data.count, "test daemon request should write")

    var response = Data()
    var buffer = [UInt8](repeating: 0, count: 4096)
    while response.firstIndex(of: 0x0a) == nil {
        let count = buffer.withUnsafeMutableBytes { rawBuffer in
            Darwin.read(fd, rawBuffer.baseAddress, rawBuffer.count)
        }
        expect(count > 0, "test daemon response should read")
        response.append(contentsOf: buffer.prefix(count))
    }
    let line = Data(response[..<(response.firstIndex(of: 0x0a) ?? response.endIndex)])
    guard let object = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else {
        expect(false, "test daemon response should decode")
        return [:]
    }
    expect(object["error"] == nil, "test daemon response should not be an error: \(object)")
    return object
}

private func connectUnixForTest(_ fd: Int32, path: String) -> Bool {
    var address = sockaddr_un()
    address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
    address.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8CString)
    let maxPathLength = MemoryLayout.size(ofValue: address.sun_path)
    guard bytes.count <= maxPathLength else { return false }
    withUnsafeMutablePointer(to: &address.sun_path) { pointer in
        pointer.withMemoryRebound(to: CChar.self, capacity: maxPathLength) { destination in
            for index in 0..<maxPathLength {
                destination[index] = 0
            }
            for index in 0..<bytes.count {
                destination[index] = bytes[index]
            }
        }
    }
    return withUnsafePointer(to: &address) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
            Darwin.connect(fd, socketAddress, socklen_t(MemoryLayout<sockaddr_un>.size)) == 0
        }
    }
}

let tests: [(String, () -> Void)] = [
    ("overlay hit testing makes whole pet body draggable", testOverlayHitTestingMakesWholePetBodyDraggable),
    ("overlay right-click opens pet browser", testOverlayRightClickRequestsPetBrowser),
    ("overlay keeps bubble through auto idle reset", testOverlayKeepsBubbleThroughAutoIdleReset),
    ("pet package maps daemon ref without manifest parsing", testPetPackageMapsDaemonRefWithoutManifestParsing),
    ("daemon snapshot decodes wire presentation", testDaemonSnapshotDecodesWirePresentation),
    ("overlay keeps daemon done bubble across idle snapshot", testOverlayKeepsDaemonDoneBubbleAcrossIdleSnapshot),
    ("debug state server is opt-in", testStateServerRequiresExplicitDebugFlag),
    ("Petdex browser bridge action allowlist", testPetdexBrowserBridgeActionAllowlist),
    ("installed pet payload uses asset scheme", testInstalledPetPayloadIsDataOnly),
    ("update status maps daemon wire states", testUpdateStatusMapsWireStates),
    ("bundled Go daemon Pi protocol socket", testBundledGoDaemonServesPiProtocolOverUnixSocket),
    ("mouse proximity dwell/cooldown", testMouseProximityRequiresDwellAndCooldown),
    ("focus + reduce motion", testFocusModeAndReduceMotionStayQuiet),
    ("click spam annoyed", testClickSpamBecomesAnnoyed),
    ("idle attention budget", testIdleAttentionBudgetCapsMicroIdle),
    ("long-running settle", testLongRunningSettlesToWaitingPose),
    ("manual animated states", testManualAnimatedStatesPlayOnce),
    ("double-click cooldown override", testDoubleClickOverridesSingleClickCooldown),
    ("directional drag running loop", testDragUsesDirectionalRunningLoop),
    ("success during happy cooldown", testSuccessEventStillAppliesDuringHappyCooldown),
    ("reduce motion off resumes running", testReduceMotionOffResumesRunningPlayback),
    ("mouse leave resets dwell", testMouseLeavingRadiusResetsDwell),
]

for (name, test) in tests {
    test()
    print("✓ \(name)")
}
print("PetBrain tests passed")
