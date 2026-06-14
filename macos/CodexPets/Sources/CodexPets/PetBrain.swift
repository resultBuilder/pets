import Cocoa
import Foundation

enum PetAttentionMode: String, CaseIterable, Equatable {
    case focus
    case `default`
    case playful

    var label: String {
        switch self {
        case .focus: return "Focus"
        case .default: return "Default"
        case .playful: return "Playful"
        }
    }

    var proximityRadius: CGFloat {
        switch self {
        case .focus: return 0
        case .default: return 150
        case .playful: return 190
        }
    }

    var minIdleActionGap: TimeInterval {
        switch self {
        case .focus: return .infinity
        case .default: return 24
        case .playful: return 12
        }
    }

    var idleBudgetPerMinute: Int {
        switch self {
        case .focus: return 0
        case .default: return 5
        case .playful: return 10
        }
    }

    var clickHappyCooldown: TimeInterval {
        switch self {
        case .focus: return 25
        case .default: return 15
        case .playful: return 8
        }
    }
}

enum PetBubbleMode: String, CaseIterable, Equatable {
    case off
    case importantOnly
    case all
    case chatty

    var label: String {
        switch self {
        case .off: return "Silent"
        case .importantOnly: return "Quiet"
        case .all: return "Default"
        case .chatty: return "Chatty"
        }
    }

    var defaultDailyMurmurLimit: Int {
        switch self {
        case .off: return 0
        case .importantOnly: return 5
        case .all: return 4
        case .chatty: return 8
        }
    }

    var defaultGlobalMurmurCooldown: TimeInterval {
        switch self {
        case .off: return .infinity
        case .importantOnly: return 90
        case .all: return 3 * 60
        case .chatty: return 90
        }
    }

    var defaultSemanticGroupCooldown: TimeInterval {
        switch self {
        case .off: return .infinity
        case .importantOnly: return 45 * 60
        case .all: return 12 * 60 * 60
        case .chatty: return 3 * 60 * 60
        }
    }
}

enum PetEventImportance: String, Equatable {
    case low
    case medium
    case high

    init(raw: String?) {
        switch raw?.lowercased() {
        case "high": self = .high
        case "medium": self = .medium
        default: self = .low
        }
    }
}

enum PetMood: String, Equatable {
    case calm
    case curious
    case happy
    case focused
    case waiting
    case sad
    case sleepy
    case annoyed
}

enum PetDragDirection: Equatable {
    case left
    case right

    init(horizontalDelta: CGFloat) {
        self = horizontalDelta < 0 ? .left : .right
    }

    var stateID: String {
        switch self {
        case .left:
            return "running-left"
        case .right:
            return "running-right"
        }
    }
}

enum PetSignal: Equatable {
    case idlePulse
    case mouseNear(distance: CGFloat)
    case mouseEntered
    case clicked(count: Int)
    case dragged(direction: PetDragDirection)
    case userInactive(seconds: TimeInterval)
    case userReturned
    case codexState(String)
    case codexEvent(type: String, label: String?, importance: PetEventImportance)
    case reduceMotionChanged(Bool)
}

enum PetPlaybackMode: Equatable {
    case staticFrame(Int)
    case playOnce
    case loop
    case loopFor(TimeInterval)
    case loopWithPause(active: TimeInterval, pause: ClosedRange<TimeInterval>)

    static func == (lhs: PetPlaybackMode, rhs: PetPlaybackMode) -> Bool {
        switch (lhs, rhs) {
        case let (.staticFrame(a), .staticFrame(b)):
            return a == b
        case (.playOnce, .playOnce):
            return true
        case (.loop, .loop):
            return true
        case let (.loopFor(a), .loopFor(b)):
            return abs(a - b) < 0.0001
        case let (.loopWithPause(aActive, aPause), .loopWithPause(bActive, bPause)):
            return abs(aActive - bActive) < 0.0001
                && abs(aPause.lowerBound - bPause.lowerBound) < 0.0001
                && abs(aPause.upperBound - bPause.upperBound) < 0.0001
        default:
            return false
        }
    }
}

struct PetDecision: Equatable {
    let mood: PetMood
    let stateID: String
    let duration: TimeInterval?
    let bubble: String?
    let playback: PetPlaybackMode
}

final class PetBrain {
    var mode: PetAttentionMode
    var bubbleMode: PetBubbleMode
    private(set) var reduceMotion: Bool

    private let now: () -> TimeInterval
    private var cooldownUntil: [PetMood: TimeInterval] = [:]
    private var clickTimes: [TimeInterval] = []
    private var nearSince: TimeInterval?
    private var idleActionTimes: [TimeInterval] = []
    private var lastIdleActionAt: TimeInterval?
    private var activeCodexState = "idle"
    private var runningStartedAt: TimeInterval?
    private var didSettleLongRunning = false
    private var lastReturnGreetingAt: TimeInterval?

    // Murmur text lives in the Go daemon (internal/petbrain); this brain
    // only decides poses, moods, and pacing for the native renderer.
    init(
        mode: PetAttentionMode = .default,
        bubbleMode: PetBubbleMode = .all,
        reduceMotion: Bool = false,
        now: @escaping () -> TimeInterval = { ProcessInfo.processInfo.systemUptime }
    ) {
        self.mode = mode
        self.bubbleMode = bubbleMode
        self.reduceMotion = reduceMotion
        self.now = now
    }

    func handle(_ signal: PetSignal) -> PetDecision? {
        switch signal {
        case .idlePulse:
            return handleIdlePulse()
        case let .mouseNear(distance):
            return handleMouseNear(distance: distance)
        case .mouseEntered:
            return handleMouseEntered()
        case let .clicked(count):
            return handleClick(count: count)
        case let .dragged(direction):
            return decision(mood: .happy, stateID: direction.stateID, duration: nil, bubble: nil, playback: .loop)
        case let .userInactive(seconds):
            guard seconds >= 8 * 60 else { return nil }
            activeCodexState = "idle"
            runningStartedAt = nil
            return decision(mood: .sleepy, stateID: "idle", duration: nil, bubble: nil, playback: .staticFrame(0))
        case .userReturned:
            return handleUserReturned()
        case let .codexState(state):
            return handleCodexState(state)
        case let .codexEvent(type, label, importance):
            return handleCodexEvent(type: type, label: label, importance: importance)
        case let .reduceMotionChanged(value):
            reduceMotion = value
            return decisionForCurrentStateAfterMotionChange()
        }
    }

    private func handleMouseNear(distance: CGFloat) -> PetDecision? {
        guard mode != .focus, distance <= mode.proximityRadius, !reduceMotion else {
            nearSince = nil
            return nil
        }

        let current = now()
        if nearSince == nil {
            nearSince = current
            return nil
        }

        guard current - (nearSince ?? current) >= 0.4 else { return nil }
        guard canUse(.curious) else { return nil }

        nearSince = current
        startCooldown(.curious, for: 5)
        return decision(mood: .curious, stateID: "waiting", duration: 1.8, bubble: nil, playback: .playOnce)
    }

    private func handleMouseEntered() -> PetDecision? {
        guard mode != .focus, canUse(.curious), !reduceMotion else { return nil }
        startCooldown(.curious, for: 5)
        return decision(mood: .curious, stateID: "waiting", duration: 1.5, bubble: nil, playback: .playOnce)
    }

    private func handleClick(count: Int) -> PetDecision? {
        let current = now()
        for _ in 0..<max(1, count) {
            clickTimes.append(current)
        }
        clickTimes = clickTimes.filter { current - $0 <= 10 }

        if clickTimes.count >= 5, canUse(.annoyed) {
            clickTimes.removeAll()
            startCooldown(.annoyed, for: 60)
            return decision(mood: .annoyed, stateID: "failed", duration: 2.4, bubble: nil, playback: .playOnce)
        }

        let isPetting = count >= 2
        guard isPetting || canUse(.happy) else { return nil }
        startCooldown(.happy, for: mode.clickHappyCooldown)
        return decision(mood: .happy, stateID: isPetting ? "jumping" : "waving", duration: isPetting ? 1.4 : 1.2, bubble: nil, playback: .playOnce)
    }

    private func handleUserReturned() -> PetDecision? {
        let current = now()
        if let lastReturnGreetingAt, current - lastReturnGreetingAt < 90 * 60 {
            return nil
        }
        guard mode != .focus, canUse(.happy) else { return nil }
        lastReturnGreetingAt = current
        startCooldown(.happy, for: mode.clickHappyCooldown)
        return decision(mood: .happy, stateID: "waving", duration: 1.4, bubble: nil, playback: .playOnce)
    }

    private func handleCodexState(_ rawState: String) -> PetDecision? {
        let state = rawState.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        activeCodexState = state

        switch state {
        case "running", "running-left", "running-right":
            if runningStartedAt == nil { runningStartedAt = now() }
            didSettleLongRunning = false
            return decision(mood: .focused, stateID: state, duration: nil, bubble: nil, playback: reduceMotion ? .staticFrame(0) : .loopWithPause(active: 4, pause: 20...45))
        case "waiting":
            runningStartedAt = nil
            return decision(mood: .waiting, stateID: "waiting", duration: 3, bubble: nil, playback: .playOnce)
        case "review":
            runningStartedAt = nil
            return decision(mood: .waiting, stateID: "review", duration: 4, bubble: nil, playback: .playOnce)
        case "failed":
            runningStartedAt = nil
            return decision(mood: .sad, stateID: "failed", duration: 2.8, bubble: nil, playback: .playOnce)
        case "waving":
            runningStartedAt = nil
            return decision(mood: .happy, stateID: "waving", duration: 1.4, bubble: nil, playback: .playOnce)
        case "jumping":
            runningStartedAt = nil
            return decision(mood: .happy, stateID: "jumping", duration: 1.4, bubble: nil, playback: .playOnce)
        case "idle":
            runningStartedAt = nil
            didSettleLongRunning = false
            return decision(mood: .calm, stateID: "idle", duration: nil, bubble: nil, playback: .staticFrame(0))
        default:
            runningStartedAt = nil
            return decision(mood: .calm, stateID: state.isEmpty ? "idle" : state, duration: nil, bubble: nil, playback: .staticFrame(0))
        }
    }

    private func handleCodexEvent(type: String, label: String?, importance: PetEventImportance) -> PetDecision? {
        let normalized = type.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let safeLabel = sanitizedLabel(label)

        switch normalized {
        case "task.succeeded", "task.success", "success":
            activeCodexState = "idle"
            runningStartedAt = nil
            didSettleLongRunning = false
            startCooldown(.happy, for: mode.clickHappyCooldown)
            return decision(
                mood: .happy,
                stateID: "waving",
                duration: 1.6,
                bubble: nil,
                playback: .playOnce
            )
        case "task.needs_user", "task.needs-user", "needs_user", "waiting":
            activeCodexState = "waiting"
            runningStartedAt = nil
            return decision(
                mood: .waiting,
                stateID: "waiting",
                duration: 3,
                bubble: nil,
                playback: .playOnce
            )
        case "review":
            activeCodexState = "review"
            runningStartedAt = nil
            return decision(
                mood: .waiting,
                stateID: "review",
                duration: 4,
                bubble: nil,
                playback: .playOnce
            )
        case "task.failed", "task.failure", "failed", "error":
            activeCodexState = "failed"
            runningStartedAt = nil
            return decision(
                mood: .sad,
                stateID: "failed",
                duration: 2.8,
                bubble: nil,
                playback: .playOnce
            )
        default:
            guard importance != .low else { return nil }
            return decision(
                mood: .waiting,
                stateID: "waiting",
                duration: 3,
                bubble: shouldBubble(importance) ? safeLabel : nil,
                playback: .playOnce
            )
        }
    }

    private func handleIdlePulse() -> PetDecision? {
        let current = now()

        if let runningStartedAt,
           !didSettleLongRunning,
           current - runningStartedAt >= 5 * 60
        {
            didSettleLongRunning = true
            activeCodexState = "waiting"
            return decision(mood: .focused, stateID: "waiting", duration: nil, bubble: nil, playback: .staticFrame(0))
        }

        guard mode != .focus, !reduceMotion else { return nil }
        guard activeCodexState == "idle" || activeCodexState == "waiting" else { return nil }

        if let lastIdleActionAt, current - lastIdleActionAt < mode.minIdleActionGap {
            return nil
        }

        idleActionTimes = idleActionTimes.filter { current - $0 < 60 }
        guard idleActionTimes.count < mode.idleBudgetPerMinute else { return nil }

        idleActionTimes.append(current)
        lastIdleActionAt = current
        return decision(mood: .curious, stateID: "idle", duration: 0.9, bubble: nil, playback: .playOnce)
    }

    private func decisionForCurrentStateAfterMotionChange() -> PetDecision {
        switch activeCodexState {
        case "running", "running-left", "running-right":
            return decision(mood: .focused, stateID: activeCodexState, duration: nil, bubble: nil, playback: .loopWithPause(active: 4, pause: 20...45))
        case "review":
            return decision(mood: .waiting, stateID: "review", duration: nil, bubble: nil, playback: .playOnce)
        case "waiting":
            return decision(mood: .waiting, stateID: "waiting", duration: nil, bubble: nil, playback: .playOnce)
        case "failed":
            return decision(mood: .sad, stateID: "failed", duration: nil, bubble: nil, playback: .playOnce)
        default:
            return decision(mood: .calm, stateID: activeCodexState == "idle" ? "idle" : activeCodexState, duration: nil, bubble: nil, playback: .staticFrame(0))
        }
    }

    private func decision(
        mood: PetMood,
        stateID: String,
        duration: TimeInterval?,
        bubble: String?,
        playback: PetPlaybackMode
    ) -> PetDecision {
        let finalPlayback: PetPlaybackMode
        if reduceMotion {
            finalPlayback = .staticFrame(0)
        } else {
            finalPlayback = playback
        }
        return PetDecision(
            mood: mood,
            stateID: stateID,
            duration: duration,
            bubble: bubble,
            playback: finalPlayback
        )
    }

    private func canUse(_ mood: PetMood) -> Bool {
        now() >= (cooldownUntil[mood] ?? -.infinity)
    }

    private func startCooldown(_ mood: PetMood, for seconds: TimeInterval) {
        cooldownUntil[mood] = now() + seconds
    }

    private func shouldBubble(_ importance: PetEventImportance) -> Bool {
        switch bubbleMode {
        case .off:
            return false
        case .importantOnly:
            return importance != .low
        case .all, .chatty:
            return true
        }
    }

    private func sanitizedLabel(_ label: String?) -> String? {
        guard let label else { return nil }
        let trimmed = label.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        return String(trimmed.prefix(120))
    }
}
