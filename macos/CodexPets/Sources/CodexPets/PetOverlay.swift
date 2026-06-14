import Cocoa

final class PetOverlayController: NSObject {
    private static let defaultWindowSize = NSSize(width: 190, height: 235)

    private let panel: NSPanel
    private let rootView: PetOverlayView
    private let brain: PetBrain

    private var idleReset: Timer?
    private var interactionTimer: Timer?
    private var idlePulseTimer: Timer?
    private var bubbleClearTimer: Timer?
    private var wasCursorOverInteractive = false
    private var isDraggingPet = false
    private var activeDragDirection: PetDragDirection?

    private(set) var currentPet: PetPackage?
    private(set) var currentStateID = "idle"
    private(set) var scale: CGFloat = 0.76
    private(set) var followsSystemReduceMotion = true
    private(set) var alwaysReduceMotion = false
    private(set) var showsInFullScreen = false

    var attentionMode: PetAttentionMode { brain.mode }
    var bubbleMode: PetBubbleMode { brain.bubbleMode }
    var reduceMotion: Bool { brain.reduceMotion }
    var currentBubbleText: String { rootView.bubbleText }
    var petBrowserRequestedHandler: (() -> Void)?
    var approvalDecisionHandler: ((String, String) -> Void)?
    /// Reports pet interactions ("click"/"drag"/"mouseNear", click count) to
    /// the daemon's brain; murmurs come back through the snapshot.
    var interactionHandler: ((String, Int) -> Void)?
    /// Requests a murmur mute (nil = rest of the day, otherwise seconds).
    var muteRequestedHandler: ((TimeInterval?) -> Void)?
    private var lastDragInteractionAt: TimeInterval = 0
    var updateRequestedHandler: (() -> Void)?

    override init() {
        self.rootView = PetOverlayView(
            frame: NSRect(origin: .zero, size: Self.defaultWindowSize)
        )
        self.panel = NSPanel(
            contentRect: NSRect(origin: .zero, size: Self.defaultWindowSize),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        self.brain = PetBrain(
            reduceMotion: NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        )
        super.init()

        panel.contentView = rootView
        panel.backgroundColor = .clear
        panel.isOpaque = false
        panel.hasShadow = false
        panel.level = .statusBar
        applyCollectionBehavior()
        panel.hidesOnDeactivate = false
        panel.worksWhenModal = true
        panel.isMovableByWindowBackground = false
        panel.ignoresMouseEvents = true
        panel.setFrameAutosaveName("CodexPetsOverlay")

        rootView.windowDragHandler = { [weak self] delta in
            self?.moveBy(delta)
        }
        rootView.dragMovedHandler = { [weak self] delta in
            self?.handleDrag(delta: delta)
        }
        rootView.dragEndedHandler = { [weak self] in
            self?.isDraggingPet = false
            self?.activeDragDirection = nil
            self?.scheduleIdleReset(after: 0.8)
            self?.updateMouseTransparencyAndProximity()
        }
        rootView.clickHandler = { [weak self] count in
            self?.handleDirectSignal(.clicked(count: count))
        }
        rootView.rightClickHandler = { [weak self] in
            self?.petBrowserRequestedHandler?()
        }
        rootView.bubbleActionHandler = { [weak self] in
            self?.dismissBubbleAndMute()
        }
        rootView.approvalDecisionHandler = { [weak self] approvalID, decision in
            self?.approvalDecisionHandler?(approvalID, decision)
        }
        rootView.updateButtonHandler = { [weak self] in
            self?.updateRequestedHandler?()
        }

        NotificationCenter.default.addObserver(
            self,
            selector: #selector(accessibilityDisplayOptionsChanged(_:)),
            name: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification,
            object: nil
        )

        placeDefault()
    }

    deinit {
        NotificationCenter.default.removeObserver(self)
        idleReset?.invalidate()
        interactionTimer?.invalidate()
        idlePulseTimer?.invalidate()
        bubbleClearTimer?.invalidate()
    }

    var isVisible: Bool {
        panel.isVisible
    }

    func show() {
        panel.orderFrontRegardless()
        panel.ignoresMouseEvents = currentPet == nil
        startInteractionPolling()
        scheduleIdlePulse()
    }

    func hide() {
        panel.orderOut(nil)
        stopInteractionPolling()
        idlePulseTimer?.invalidate()
        idlePulseTimer = nil
        panel.ignoresMouseEvents = true
    }

    func toggle() {
        isVisible ? hide() : show()
    }

    func setPet(_ pet: PetPackage?) {
        currentPet = pet
        currentStateID = "idle"
        rootView.setPet(
            pet,
            state: PetAnimationState.named("idle", from: pet?.states ?? PetAnimationState.defaults),
            scale: scale,
            playback: .staticFrame(0)
        )
        resizeForScale()
        if pet != nil {
            show()
        } else {
            hide()
        }
    }

    func setScale(_ newScale: CGFloat) {
        scale = newScale
        rootView.scale = newScale
        resizeForScale()
        updateMouseTransparencyAndProximity()
    }

    func setState(_ id: String, duration: TimeInterval? = nil) {
        if let decision = brain.handle(.codexState(id)) {
            apply(decision, resetOverride: duration)
        } else {
            directSetState(id, playback: playbackMode(for: id, duration: duration))
            if let duration, duration > 0, id != "idle" {
                scheduleIdleReset(after: duration)
            }
        }
    }

    func setUpdateStatus(_ status: UpdateStatus) {
        switch status {
        case .idle, .checking:
            rootView.updateButtonText = nil
        case let .available(commits):
            rootView.updateButtonText = commits > 0 ? "Update (\(commits))" : "Rebuild"
        case let .updating(stage):
            rootView.updateButtonText = stage
        case let .failed(message):
            rootView.updateButtonText = "Retry"
            setBubble("Update failed: \(String(message.prefix(80)))", autoClearAfter: 8)
        case .restarting:
            rootView.updateButtonText = "Restarting…"
        }
        updateMouseTransparencyAndProximity()
    }

    func applyDaemonSnapshot(_ snapshot: DaemonSnapshot) {
        let presentation = snapshot.presentationOrIdle
        rootView.pendingApprovalID = snapshot.pendingApprovals.first { $0.state == "pending" }?.id
        // A drag owns the pose: a snapshot arriving mid-drag must not stomp
        // the directional run animation. Drag end restores the daemon state.
        if isDraggingPet {
            if let bubble = presentation.bubble {
                setBubble(bubble, autoClearAfter: presentation.autoClearAfter)
            }
            return
        }
        let preserveAutoClearingBubble = presentation.stateId == "idle"
            && presentation.bubble == nil
            && hasAutoClearingBubble
        if preserveAutoClearingBubble {
            setState(presentation.stateId, preservingBubble: true)
        } else {
            setState(presentation.stateId)
        }
        if let bubble = presentation.bubble {
            setBubble(bubble, autoClearAfter: presentation.autoClearAfter)
        } else if presentation.stateId == "idle", !preserveAutoClearingBubble {
            setBubble("", autoClearAfter: nil)
        }
    }

    func setEvent(type: String, label: String?, importance: PetEventImportance) {
        guard let decision = brain.handle(.codexEvent(type: type, label: label, importance: importance)) else { return }
        apply(decision)
    }

    func setBubble(_ text: String, autoClearAfter: TimeInterval? = 6) {
        bubbleClearTimer?.invalidate()
        bubbleClearTimer = nil
        rootView.bubbleText = text

        if let autoClearAfter, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            bubbleClearTimer = oneShotTimer(after: autoClearAfter) { [weak self] in
                guard let self else { return }
                self.rootView.bubbleText = ""
                self.bubbleClearTimer = nil
                self.updateMouseTransparencyAndProximity()
            }
        }
        updateMouseTransparencyAndProximity()
    }

    func muteMurmursForToday() {
        muteRequestedHandler?(nil)
        setBubble("")
    }

    func applySettings(
        attentionMode: PetAttentionMode,
        bubbleMode: PetBubbleMode,
        followsSystemReduceMotion: Bool,
        alwaysReduceMotion: Bool,
        showsInFullScreen: Bool
    ) {
        brain.mode = attentionMode
        brain.bubbleMode = bubbleMode
        self.followsSystemReduceMotion = followsSystemReduceMotion
        self.alwaysReduceMotion = alwaysReduceMotion
        self.showsInFullScreen = showsInFullScreen
        applyCollectionBehavior()
        updateReduceMotion()
        rescheduleIdlePulse()
    }

    func setAttentionMode(_ mode: PetAttentionMode) {
        brain.mode = mode
        rescheduleIdlePulse()
    }

    func setBubbleMode(_ mode: PetBubbleMode) {
        brain.bubbleMode = mode
        if mode == .off {
            setBubble("")
        }
    }

    func setFollowsSystemReduceMotion(_ value: Bool) {
        followsSystemReduceMotion = value
        updateReduceMotion()
    }

    func setAlwaysReduceMotion(_ value: Bool) {
        alwaysReduceMotion = value
        updateReduceMotion()
    }

    func setShowsInFullScreen(_ value: Bool) {
        showsInFullScreen = value
        applyCollectionBehavior()
    }

    private func handleDirectSignal(_ signal: PetSignal) {
        forwardInteraction(signal)
        guard let decision = brain.handle(signal) else { return }
        apply(decision)
    }

    /// The daemon's brain owns murmurs; the local brain only paces poses,
    /// so interactions are mirrored over the socket.
    private func forwardInteraction(_ signal: PetSignal) {
        switch signal {
        case let .clicked(count):
            interactionHandler?("click", count)
        case .dragged:
            let now = ProcessInfo.processInfo.systemUptime
            guard now - lastDragInteractionAt > 5 else { return }
            lastDragInteractionAt = now
            interactionHandler?("drag", 0)
        case .mouseNear, .mouseEntered:
            interactionHandler?("mouseNear", 0)
        case .userReturned:
            interactionHandler?("userReturned", 0)
        default:
            break
        }
    }

    private func dismissBubbleAndMute() {
        guard !rootView.bubbleText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        muteRequestedHandler?(3 * 60 * 60)
        setBubble("")
    }

    private func apply(
        _ decision: PetDecision,
        resetOverride: TimeInterval? = nil,
        preserveExistingBubble: Bool = false
    ) {
        guard currentPet != nil else { return }
        directSetState(decision.stateID, playback: decision.playback)

        if let bubble = decision.bubble {
            setBubble(bubble, autoClearAfter: decision.mood == .waiting ? 8 : 4)
        } else if !preserveExistingBubble && (decision.mood == .focused || decision.mood == .calm) {
            setBubble("")
        }

        if let resetOverride, resetOverride > 0, decision.stateID != "idle" {
            scheduleIdleReset(after: resetOverride)
        } else if shouldAutoReset(decision), let duration = decision.duration, duration > 0 {
            scheduleIdleReset(after: duration)
        }
    }

    private func shouldAutoReset(_ decision: PetDecision) -> Bool {
        switch decision.mood {
        case .happy, .sad, .annoyed, .curious:
            return true
        case .calm, .focused, .waiting, .sleepy:
            return false
        }
    }

    private func directSetState(_ id: String, playback: PetPlaybackMode) {
        idleReset?.invalidate()
        idleReset = nil
        guard let pet = currentPet else { return }
        currentStateID = id
        let state = PetAnimationState.named(id, from: pet.states)
        rootView.setState(state, playback: playback)
    }

    private func playbackMode(for id: String, duration: TimeInterval?) -> PetPlaybackMode {
        if reduceMotion { return .staticFrame(0) }
        if id == "idle" { return .staticFrame(0) }
        if id.hasPrefix("running") {
            if let duration, duration > 0 { return .loopFor(min(duration, 8)) }
            return .loopWithPause(active: 4, pause: 20...45)
        }
        if ["waving", "jumping", "failed", "waiting", "review"].contains(id) {
            return .playOnce
        }
        return .staticFrame(0)
    }

    private func scheduleIdleReset(after delay: TimeInterval) {
        idleReset?.invalidate()
        idleReset = oneShotTimer(after: delay) { [weak self] in
            self?.setState("idle", preservingBubble: true)
        }
    }

    private var hasAutoClearingBubble: Bool {
        bubbleClearTimer != nil && !rootView.bubbleText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func setState(_ id: String, preservingBubble: Bool) {
        if let decision = brain.handle(.codexState(id)) {
            apply(decision, preserveExistingBubble: preservingBubble)
        } else {
            directSetState(id, playback: playbackMode(for: id, duration: nil))
        }
    }

    private func resizeForScale() {
        let baseWidth = Self.defaultWindowSize.width
        let baseHeight = Self.defaultWindowSize.height
        let frame = panel.frame
        let nextSize = NSSize(
            width: max(baseWidth, 170 * scale + 50),
            height: max(baseHeight, 190 * scale + 80)
        )
        let nextFrame = NSRect(
            x: frame.midX - nextSize.width / 2,
            y: frame.maxY - nextSize.height,
            width: nextSize.width,
            height: nextSize.height
        )
        panel.setFrame(nextFrame, display: true)
        rootView.frame = NSRect(origin: .zero, size: nextSize)
    }

    private func placeDefault() {
        guard let screen = NSScreen.main else { return }
        let visible = screen.visibleFrame
        let frame = panel.frame
        let origin = NSPoint(
            x: visible.maxX - frame.width - 34,
            y: visible.minY + 42
        )
        panel.setFrameOrigin(origin)
    }

    private func moveBy(_ delta: NSPoint) {
        var frame = panel.frame
        frame.origin.x += delta.x
        frame.origin.y -= delta.y
        panel.setFrame(frame, display: true)
        updateMouseTransparencyAndProximity()
    }

    private func handleDrag(delta: NSPoint) {
        isDraggingPet = true
        let direction: PetDragDirection
        if abs(delta.x) >= 0.5 {
            direction = PetDragDirection(horizontalDelta: delta.x)
        } else if let activeDragDirection {
            direction = activeDragDirection
        } else {
            direction = .right
        }

        guard activeDragDirection != direction || !currentStateID.hasPrefix("running") else { return }
        activeDragDirection = direction
        handleDirectSignal(.dragged(direction: direction))
    }

    private func applyCollectionBehavior() {
        var behavior: NSWindow.CollectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        if showsInFullScreen {
            behavior.insert(.fullScreenAuxiliary)
        }
        panel.collectionBehavior = behavior
    }

    private func updateReduceMotion() {
        let shouldReduce = alwaysReduceMotion || (followsSystemReduceMotion && NSWorkspace.shared.accessibilityDisplayShouldReduceMotion)
        guard let decision = brain.handle(.reduceMotionChanged(shouldReduce)) else { return }
        apply(decision)
    }

    @objc private func accessibilityDisplayOptionsChanged(_ notification: Notification) {
        updateReduceMotion()
    }

    private func startInteractionPolling() {
        guard interactionTimer == nil else { return }
        interactionTimer = Timer(timeInterval: 0.18, repeats: true) { [weak self] _ in
            self?.updateMouseTransparencyAndProximity()
        }
        RunLoop.main.add(interactionTimer!, forMode: .common)
    }

    private func stopInteractionPolling() {
        interactionTimer?.invalidate()
        interactionTimer = nil
        wasCursorOverInteractive = false
    }

    private func updateMouseTransparencyAndProximity() {
        guard panel.isVisible, currentPet != nil else {
            panel.ignoresMouseEvents = true
            wasCursorOverInteractive = false
            return
        }

        guard !isDraggingPet else { return }

        let screenPoint = NSEvent.mouseLocation
        let windowPoint = panel.convertPoint(fromScreen: screenPoint)
        let localPoint = rootView.convert(windowPoint, from: nil)
        let cursorOverInteractive = rootView.containsInteractivePoint(localPoint)

        if cursorOverInteractive, !wasCursorOverInteractive {
            handleDirectSignal(.mouseEntered)
        }
        wasCursorOverInteractive = cursorOverInteractive

        let distance = rootView.distanceFromSprite(to: localPoint)
        if distance.isFinite {
            handleDirectSignal(.mouseNear(distance: distance))
        }
    }

    private func scheduleIdlePulse(after delay: TimeInterval? = nil) {
        idlePulseTimer?.invalidate()
        guard panel.isVisible, currentPet != nil else {
            idlePulseTimer = nil
            return
        }

        let interval = delay ?? nextIdlePulseInterval()
        idlePulseTimer = oneShotTimer(after: interval) { [weak self] in
            guard let self else { return }
            self.handleDirectSignal(.idlePulse)
            self.scheduleIdlePulse()
        }
    }

    private func rescheduleIdlePulse() {
        idlePulseTimer?.invalidate()
        idlePulseTimer = nil
        scheduleIdlePulse(after: 1)
    }

    private func nextIdlePulseInterval() -> TimeInterval {
        switch attentionMode {
        case .focus:
            return 90
        case .default:
            return Double.random(in: 12...35)
        case .playful:
            return Double.random(in: 6...18)
        }
    }

    private func oneShotTimer(after delay: TimeInterval, _ block: @escaping () -> Void) -> Timer {
        let timer = Timer(timeInterval: max(0.05, delay), repeats: false) { _ in block() }
        RunLoop.main.add(timer, forMode: .common)
        return timer
    }
}

final class PetOverlayView: NSView {
    var windowDragHandler: ((NSPoint) -> Void)?
    var clickHandler: ((Int) -> Void)?
    var rightClickHandler: (() -> Void)?
    var dragMovedHandler: ((NSPoint) -> Void)?
    var dragEndedHandler: (() -> Void)?
    var bubbleActionHandler: (() -> Void)?
    var approvalDecisionHandler: ((String, String) -> Void)?
    var updateButtonHandler: (() -> Void)?

    var scale: CGFloat = 1.0 {
        didSet { needsDisplay = true }
    }
    var bubbleText: String = "" {
        didSet { needsDisplay = true }
    }
    var pendingApprovalID: String? {
        didSet { needsDisplay = true }
    }
    var updateButtonText: String? {
        didSet { needsDisplay = true }
    }

    private var pet: PetPackage?
    private var spriteImage: NSImage?
    private var currentState = PetAnimationState.defaults[0]
    private var frameIndex = 0
    private var frameTimer: Timer?
    private var phaseTimer: Timer?
    private var isDraggingPet = false
    private var mouseDownStartedInBubble = false

    override var isFlipped: Bool { true }

    var spriteRect: NSRect {
        guard let pet else { return .zero }
        let frameWidth = CGFloat(pet.frameWidth)
        let frameHeight = CGFloat(pet.frameHeight)
        let drawWidth = frameWidth * 0.72 * scale
        let drawHeight = frameHeight * 0.72 * scale
        return NSRect(
            x: (bounds.width - drawWidth) / 2,
            y: bounds.height - drawHeight - 18,
            width: drawWidth,
            height: drawHeight
        )
    }

    var petBodyHitRect: NSRect {
        pet == nil ? .zero : bounds
    }

    var bubbleRect: NSRect {
        guard !bubbleText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return .zero }
        let hasApprovalActions = pendingApprovalID != nil
        let paragraph = NSMutableParagraphStyle()
        paragraph.alignment = .center
        paragraph.lineBreakMode = .byWordWrapping
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 12, weight: .semibold),
            .paragraphStyle: paragraph,
        ]
        let text = NSString(string: bubbleText)
        let maxWidth = min(bounds.width - 28, hasApprovalActions ? 170 : 210)
        let maxSize = NSSize(width: maxWidth, height: hasApprovalActions ? 58 : 80)
        let textSize = text.boundingRect(
            with: maxSize,
            options: [.usesLineFragmentOrigin, .usesFontLeading],
            attributes: attributes
        ).size
        let minWidth = hasApprovalActions ? min(bounds.width - 18, 164) : 0
        let width = min(bounds.width - 18, max(textSize.width + 22, minWidth))
        return NSRect(
            x: (bounds.width - width) / 2,
            y: 8,
            width: width,
            height: textSize.height + 12 + (hasApprovalActions ? 34 : 0)
        )
    }

    private var approvalButtonRects: (approve: NSRect, deny: NSRect)? {
        guard pendingApprovalID != nil else { return nil }
        let bubble = bubbleRect
        guard !bubble.isEmpty else { return nil }
        let gap: CGFloat = 8
        let buttonHeight: CGFloat = 22
        let innerWidth = max(0, bubble.width - 22)
        let buttonWidth = max(48, (innerWidth - gap) / 2)
        let y = bubble.maxY - buttonHeight - 6
        let approve = NSRect(x: bubble.minX + 11, y: y, width: buttonWidth, height: buttonHeight)
        let deny = NSRect(x: approve.maxX + gap, y: y, width: buttonWidth, height: buttonHeight)
        return (approve, deny)
    }

    var updateButtonRect: NSRect {
        guard let text = updateButtonText, !text.isEmpty else { return .zero }
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 11, weight: .bold),
        ]
        let textSize = text.size(withAttributes: attributes)
        let width = max(textSize.width + 24, 90)
        let height: CGFloat = 24
        let sprite = spriteRect
        // Place above the sprite (flipped coords: smaller y = higher on screen)
        let y = sprite.isEmpty ? 8 : sprite.minY - height - 4
        return NSRect(
            x: (bounds.width - width) / 2,
            y: max(2, y),
            width: width,
            height: height
        )
    }

    func containsInteractivePoint(_ point: NSPoint) -> Bool {
        if containsPetBodyPoint(point) {
            return true
        }

        if bubbleActionHandler != nil {
            let paddedBubble = bubbleRect.insetBy(dx: -6, dy: -6)
            if !paddedBubble.isEmpty, paddedBubble.contains(point) {
                return true
            }
        }

        let updateRect = updateButtonRect
        if !updateRect.isEmpty, updateRect.contains(point) {
            return true
        }

        return false
    }

    func containsPetBodyPoint(_ point: NSPoint) -> Bool {
        guard petBodyHitRect.contains(point) else { return false }
        if bubbleActionHandler != nil {
            let paddedBubble = bubbleRect.insetBy(dx: -6, dy: -6)
            if !paddedBubble.isEmpty, paddedBubble.contains(point) {
                return false
            }
        }
        let updateRect = updateButtonRect
        if !updateRect.isEmpty, updateRect.contains(point) {
            return false
        }
        return true
    }

    func distanceFromSprite(to point: NSPoint) -> CGFloat {
        let rect = spriteRect
        guard !rect.isEmpty else { return .infinity }
        if rect.contains(point) { return 0 }
        let dx = max(rect.minX - point.x, 0, point.x - rect.maxX)
        let dy = max(rect.minY - point.y, 0, point.y - rect.maxY)
        return sqrt(dx * dx + dy * dy)
    }

    func setPet(_ pet: PetPackage?, state: PetAnimationState, scale: CGFloat, playback: PetPlaybackMode) {
        self.pet = pet
        self.scale = scale
        self.spriteImage = pet.flatMap { NSImage(contentsOf: $0.spritesheet) }
        setState(state, playback: playback)
    }

    func setState(_ state: PetAnimationState, playback: PetPlaybackMode) {
        currentState = state
        frameIndex = 0
        startPlayback(playback)
        needsDisplay = true
    }

    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)
        NSColor.clear.setFill()
        dirtyRect.fill()

        if !bubbleText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            drawBubble()
        }

        guard let pet, let spriteImage else {
            drawEmptyHint()
            return
        }

        drawSprite(pet: pet, image: spriteImage)
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        containsInteractivePoint(point) ? self : nil
    }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool {
        true
    }

    override func mouseDown(with event: NSEvent) {
        if event.type == .rightMouseDown { return }
        let point = convert(event.locationInWindow, from: nil)
        isDraggingPet = false
        mouseDownStartedInBubble = !bubbleRect.isEmpty && bubbleRect.contains(point)
    }

    override func mouseUp(with event: NSEvent) {
        let point = convert(event.locationInWindow, from: nil)
        defer {
            mouseDownStartedInBubble = false
        }
        if isDraggingPet {
            isDraggingPet = false
            dragEndedHandler?()
        } else if let approvalDecision = approvalDecision(at: point) {
            approvalDecisionHandler?(approvalDecision.approvalID, approvalDecision.decision)
        } else if !updateButtonRect.isEmpty, updateButtonRect.contains(point) {
            updateButtonHandler?()
        } else if mouseDownStartedInBubble && !bubbleRect.contains(point) {
            return
        } else if pendingApprovalID != nil, bubbleRect.contains(point) {
            return
        } else if bubbleActionHandler != nil, bubbleRect.contains(point) {
            bubbleActionHandler?()
        } else {
            clickHandler?(max(1, event.clickCount))
        }
    }

    override func mouseDragged(with event: NSEvent) {
        if mouseDownStartedInBubble {
            return
        }
        let delta = NSPoint(x: event.deltaX, y: event.deltaY)
        if !isDraggingPet {
            isDraggingPet = true
        }
        dragMovedHandler?(delta)
        windowDragHandler?(delta)
    }

    override func rightMouseDown(with event: NSEvent) {
        handleRightClick(at: convert(event.locationInWindow, from: nil))
    }

    func handleRightClick(at point: NSPoint, activateApp: Bool = true) {
        guard containsPetBodyPoint(point) else { return }
        if activateApp {
            NSApp.activate(ignoringOtherApps: true)
        }
        rightClickHandler?()
    }

    func approvalDecision(at point: NSPoint) -> (approvalID: String, decision: String)? {
        guard let approvalID = pendingApprovalID, let buttons = approvalButtonRects else { return nil }
        if buttons.approve.contains(point) {
            return (approvalID, "approved")
        }
        if buttons.deny.contains(point) {
            return (approvalID, "denied")
        }
        return nil
    }

    private func startPlayback(_ mode: PetPlaybackMode) {
        invalidatePlayback()
        switch mode {
        case let .staticFrame(index):
            frameIndex = min(max(0, index), max(0, currentState.frames - 1))
            needsDisplay = true
        case .playOnce:
            playOnce()
        case .loop:
            startActiveLoop()
        case let .loopFor(duration):
            loopFor(duration)
        case let .loopWithPause(active, pause):
            loopWithPause(active: active, pause: pause)
        }
    }

    private func playOnce() {
        guard currentState.frames > 1 else { return }
        frameTimer = Timer(timeInterval: max(0.05, currentState.duration), repeats: true) { [weak self] _ in
            guard let self else { return }
            if self.frameIndex >= max(0, self.currentState.frames - 1) {
                self.stopAtStaticFrame()
                return
            }
            self.frameIndex += 1
            self.needsDisplay = true
        }
        RunLoop.main.add(frameTimer!, forMode: .common)
    }

    private func loopFor(_ duration: TimeInterval) {
        guard currentState.frames > 1, duration > 0 else { return }
        let end = ProcessInfo.processInfo.systemUptime + duration
        frameTimer = Timer(timeInterval: max(0.05, currentState.duration), repeats: true) { [weak self] _ in
            guard let self else { return }
            if ProcessInfo.processInfo.systemUptime >= end {
                self.stopAtStaticFrame()
                return
            }
            self.advanceFrame()
        }
        RunLoop.main.add(frameTimer!, forMode: .common)
    }

    private func loopWithPause(active: TimeInterval, pause: ClosedRange<TimeInterval>) {
        guard currentState.frames > 1, active > 0 else { return }
        startActiveLoop()
        phaseTimer = Timer(timeInterval: active, repeats: false) { [weak self] _ in
            guard let self else { return }
            self.frameTimer?.invalidate()
            self.frameTimer = nil
            self.frameIndex = 0
            self.needsDisplay = true
            let pauseSeconds = Double.random(in: pause)
            self.phaseTimer = Timer(timeInterval: pauseSeconds, repeats: false) { [weak self] _ in
                self?.loopWithPause(active: active, pause: pause)
            }
            RunLoop.main.add(self.phaseTimer!, forMode: .common)
        }
        RunLoop.main.add(phaseTimer!, forMode: .common)
    }

    private func startActiveLoop() {
        frameTimer?.invalidate()
        frameTimer = Timer(timeInterval: max(0.05, currentState.duration), repeats: true) { [weak self] _ in
            self?.advanceFrame()
        }
        RunLoop.main.add(frameTimer!, forMode: .common)
    }

    private func advanceFrame() {
        frameIndex = (frameIndex + 1) % max(1, currentState.frames)
        needsDisplay = true
    }

    private func stopAtStaticFrame() {
        frameTimer?.invalidate()
        frameTimer = nil
        frameIndex = 0
        needsDisplay = true
    }

    private func invalidatePlayback() {
        frameTimer?.invalidate()
        phaseTimer?.invalidate()
        frameTimer = nil
        phaseTimer = nil
    }

    private func drawSprite(pet: PetPackage, image: NSImage) {
        guard let context = NSGraphicsContext.current else { return }
        context.imageInterpolation = .none

        let frameWidth = CGFloat(pet.frameWidth)
        let frameHeight = CGFloat(pet.frameHeight)
        let target = spriteRect

        let imageHeight = image.size.height > 1 ? image.size.height : frameHeight * 9
        let sourceY = max(0, imageHeight - CGFloat(currentState.row + 1) * frameHeight)
        let source = NSRect(
            x: CGFloat(frameIndex % max(1, currentState.frames)) * frameWidth,
            y: sourceY,
            width: frameWidth,
            height: frameHeight
        )

        image.draw(in: target, from: source, operation: .sourceOver, fraction: 1.0, respectFlipped: true, hints: nil)

        if updateButtonText != nil {
            drawUpdateButton()
        }
    }

    private func drawBubble() {
        let paragraph = NSMutableParagraphStyle()
        paragraph.alignment = .center
        paragraph.lineBreakMode = .byWordWrapping

        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 12, weight: .semibold),
            .foregroundColor: NSColor(calibratedWhite: 0.08, alpha: 1),
            .paragraphStyle: paragraph,
        ]
        let text = NSString(string: bubbleText)
        let bubbleRect = self.bubbleRect

        NSColor.white.withAlphaComponent(0.96).setFill()
        let path = NSBezierPath(roundedRect: bubbleRect, xRadius: 12, yRadius: 12)
        path.fill()

        NSColor.black.withAlphaComponent(0.22).setStroke()
        path.lineWidth = 1
        path.stroke()

        var textRect = bubbleRect.insetBy(dx: 11, dy: 6)
        if approvalButtonRects != nil {
            textRect.size.height = max(0, textRect.height - 30)
        }
        text.draw(
            with: textRect,
            options: [.usesLineFragmentOrigin, .usesFontLeading],
            attributes: attributes
        )

        if let buttons = approvalButtonRects {
            drawApprovalButton(title: "Approve", rect: buttons.approve, fill: NSColor(calibratedRed: 0.08, green: 0.48, blue: 0.28, alpha: 1))
            drawApprovalButton(title: "Deny", rect: buttons.deny, fill: NSColor(calibratedWhite: 0.18, alpha: 1))
        }
    }

    private func drawApprovalButton(title: String, rect: NSRect, fill: NSColor) {
        fill.setFill()
        let path = NSBezierPath(roundedRect: rect, xRadius: 6, yRadius: 6)
        path.fill()
        NSColor.black.withAlphaComponent(0.22).setStroke()
        path.lineWidth = 1
        path.stroke()

        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 11, weight: .bold),
            .foregroundColor: NSColor.white,
        ]
        let textSize = title.size(withAttributes: attributes)
        title.draw(
            at: NSPoint(
                x: rect.midX - textSize.width / 2,
                y: rect.midY - textSize.height / 2
            ),
            withAttributes: attributes
        )
    }

    private func drawUpdateButton() {
        let rect = updateButtonRect
        guard !rect.isEmpty, let text = updateButtonText, !text.isEmpty else { return }

        let fill = NSColor(calibratedRed: 0.18, green: 0.52, blue: 0.95, alpha: 0.92)
        fill.setFill()
        let path = NSBezierPath(roundedRect: rect, xRadius: 6, yRadius: 6)
        path.fill()

        NSColor.white.withAlphaComponent(0.28).setStroke()
        path.lineWidth = 1
        path.stroke()

        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 11, weight: .bold),
            .foregroundColor: NSColor.white,
        ]
        let textSize = text.size(withAttributes: attributes)
        text.draw(
            at: NSPoint(
                x: rect.midX - textSize.width / 2,
                y: rect.midY - textSize.height / 2
            ),
            withAttributes: attributes
        )
    }

    private func drawEmptyHint() {
        let text = "Import a pet from the CP menu"
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 13, weight: .semibold),
            .foregroundColor: NSColor.white.withAlphaComponent(0.9),
        ]
        let size = text.size(withAttributes: attributes)
        let rect = NSRect(
            x: (bounds.width - size.width - 24) / 2,
            y: (bounds.height - size.height - 16) / 2,
            width: size.width + 24,
            height: size.height + 16
        )
        NSColor.black.withAlphaComponent(0.55).setFill()
        NSBezierPath(roundedRect: rect, xRadius: 8, yRadius: 8).fill()
        text.draw(at: NSPoint(x: rect.minX + 12, y: rect.minY + 8), withAttributes: attributes)
    }
}
