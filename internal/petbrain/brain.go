package petbrain

import (
	"math"
	"math/rand"
	"strings"
	"time"
)

// AttentionMode tunes how lively the pet is, mirroring the macOS setting.
type AttentionMode string

const (
	AttentionFocus   AttentionMode = "focus"
	AttentionDefault AttentionMode = "default"
	AttentionPlayful AttentionMode = "playful"
)

func ParseAttentionMode(value string) AttentionMode {
	switch AttentionMode(strings.ToLower(strings.TrimSpace(value))) {
	case AttentionFocus:
		return AttentionFocus
	case AttentionPlayful:
		return AttentionPlayful
	default:
		return AttentionDefault
	}
}

func (m AttentionMode) minIdleActionGap() float64 {
	switch m {
	case AttentionFocus:
		return math.Inf(1)
	case AttentionPlayful:
		return 12
	default:
		return 24
	}
}

func (m AttentionMode) idleBudgetPerMinute() int {
	switch m {
	case AttentionFocus:
		return 0
	case AttentionPlayful:
		return 10
	default:
		return 5
	}
}

func (m AttentionMode) clickHappyCooldown() float64 {
	switch m {
	case AttentionFocus:
		return 25
	case AttentionPlayful:
		return 8
	default:
		return 15
	}
}

// BubbleMode tunes how chatty the pet is.
type BubbleMode string

const (
	BubbleOff           BubbleMode = "off"
	BubbleImportantOnly BubbleMode = "importantOnly"
	BubbleAll           BubbleMode = "all"
	BubbleChatty        BubbleMode = "chatty"
)

func ParseBubbleMode(value string) BubbleMode {
	switch BubbleMode(strings.TrimSpace(value)) {
	case BubbleOff:
		return BubbleOff
	case BubbleImportantOnly:
		return BubbleImportantOnly
	case BubbleChatty:
		return BubbleChatty
	default:
		return BubbleAll
	}
}

func (m BubbleMode) defaultDailyMurmurLimit() int {
	switch m {
	case BubbleOff:
		return 0
	case BubbleImportantOnly:
		return 5
	case BubbleChatty:
		return 8
	default:
		return 4
	}
}

func (m BubbleMode) defaultGlobalMurmurCooldown() float64 {
	switch m {
	case BubbleOff:
		return math.Inf(1)
	case BubbleImportantOnly, BubbleChatty:
		return 90
	default:
		return 3 * 60
	}
}

func (m BubbleMode) defaultSemanticGroupCooldown() float64 {
	switch m {
	case BubbleOff:
		return math.Inf(1)
	case BubbleImportantOnly:
		return 45 * 60
	case BubbleChatty:
		return 3 * 60 * 60
	default:
		return 12 * 60 * 60
	}
}

type Mood string

const (
	MoodCalm    Mood = "calm"
	MoodCurious Mood = "curious"
	MoodHappy   Mood = "happy"
	MoodFocused Mood = "focused"
	MoodWaiting Mood = "waiting"
	MoodSad     Mood = "sad"
	MoodSleepy  Mood = "sleepy"
	MoodAnnoyed Mood = "annoyed"
)

// Decision is what the pet wants to express in response to a signal.
type Decision struct {
	Mood     Mood
	StateID  string
	Duration float64 // seconds the transient state lasts; 0 means steady
	Bubble   string
}

// Brain is the pet's reusable temperament: murmur selection, mood cooldowns,
// click/spam handling, idle pacing. It is not safe for concurrent use; the
// daemon store serializes access.
type Brain struct {
	Mode         AttentionMode
	BubbleModeV  BubbleMode
	ReduceMotion bool

	now         func() float64 // monotonic seconds for cooldowns
	dialogueNow func() float64 // wall clock for murmur history
	engine      *Engine
	history     *History
	onHistory   func(*History)

	cooldownUntil        map[Mood]float64
	clickTimes           []float64
	idleActionTimes      []float64
	lastIdleActionAt     *float64
	activeCodexState     string
	runningStartedAt     *float64
	didSettleLongRunning bool
	lastReturnGreetingAt *float64
}

// Options configures a Brain; zero values pick production defaults.
type Options struct {
	Mode         AttentionMode
	BubbleMode   BubbleMode
	ReduceMotion bool
	Now          func() float64
	DialogueNow  func() float64
	Lines        []Line
	Random       func() float64
	History      *History
	// OnHistory observes murmur-history changes for persistence.
	OnHistory func(*History)
}

func New(options Options) *Brain {
	if options.Mode == "" {
		options.Mode = AttentionDefault
	}
	if options.BubbleMode == "" {
		options.BubbleMode = BubbleAll
	}
	if options.Now == nil {
		start := time.Now()
		options.Now = func() float64 { return time.Since(start).Seconds() }
	}
	if options.DialogueNow == nil {
		options.DialogueNow = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	if options.Lines == nil {
		options.Lines = DefaultLines
	}
	if options.Random == nil {
		options.Random = rand.Float64
	}
	if options.History == nil {
		options.History = NewHistory()
	}
	return &Brain{
		Mode:             options.Mode,
		BubbleModeV:      options.BubbleMode,
		ReduceMotion:     options.ReduceMotion,
		now:              options.Now,
		dialogueNow:      options.DialogueNow,
		engine:           NewEngine(options.Lines, options.DialogueNow, options.Random),
		history:          options.History,
		onHistory:        options.OnHistory,
		cooldownUntil:    map[Mood]float64{},
		activeCodexState: "idle",
	}
}

// HandleCodexState reacts to the overlay state derived from the daemon
// snapshot. It returns a murmur-bearing decision only on fresh transitions.
func (b *Brain) HandleCodexState(rawState string) *Decision {
	state := strings.ToLower(strings.TrimSpace(rawState))
	previous := b.activeCodexState
	b.activeCodexState = state

	switch state {
	case "running", "running-left", "running-right":
		if b.runningStartedAt == nil {
			at := b.now()
			b.runningStartedAt = &at
		}
		b.didSettleLongRunning = false
		bubble := ""
		if previous != state && !strings.HasPrefix(previous, "running") {
			bubble = b.murmur(EventCodexRunning, MoodFocused)
		}
		return &Decision{Mood: MoodFocused, StateID: state, Bubble: bubble}
	case "waiting":
		b.runningStartedAt = nil
		bubble := ""
		if previous != state {
			bubble = b.murmur(EventCodexWaiting, MoodWaiting)
		}
		return &Decision{Mood: MoodWaiting, StateID: "waiting", Duration: 3, Bubble: bubble}
	case "review":
		b.runningStartedAt = nil
		bubble := ""
		if previous != state {
			bubble = b.murmur(EventCodexReview, MoodWaiting)
		}
		return &Decision{Mood: MoodWaiting, StateID: "review", Duration: 4, Bubble: bubble}
	case "failed":
		b.runningStartedAt = nil
		bubble := ""
		if previous != state {
			bubble = b.murmur(EventCodexFailed, MoodSad)
		}
		return &Decision{Mood: MoodSad, StateID: "failed", Duration: 2.8, Bubble: bubble}
	case "waving":
		b.runningStartedAt = nil
		bubble := ""
		if previous != state {
			bubble = b.murmur(EventCodexSuccess, MoodHappy)
		}
		return &Decision{Mood: MoodHappy, StateID: "waving", Duration: 1.4, Bubble: bubble}
	case "jumping":
		b.runningStartedAt = nil
		return &Decision{Mood: MoodHappy, StateID: "jumping", Duration: 1.4}
	case "idle":
		b.runningStartedAt = nil
		b.didSettleLongRunning = false
		return &Decision{Mood: MoodCalm, StateID: "idle"}
	default:
		b.runningStartedAt = nil
		if state == "" {
			state = "idle"
		}
		return &Decision{Mood: MoodCalm, StateID: state}
	}
}

// HandleClick reacts to count clicks landing on the pet body.
func (b *Brain) HandleClick(count int) *Decision {
	current := b.now()
	if count < 1 {
		count = 1
	}
	for i := 0; i < count; i++ {
		b.clickTimes = append(b.clickTimes, current)
	}
	recent := b.clickTimes[:0]
	for _, at := range b.clickTimes {
		if current-at <= 10 {
			recent = append(recent, at)
		}
	}
	b.clickTimes = recent

	if len(b.clickTimes) >= 5 && b.canUse(MoodAnnoyed) {
		b.clickTimes = nil
		b.startCooldown(MoodAnnoyed, 60)
		return &Decision{
			Mood:     MoodAnnoyed,
			StateID:  "failed",
			Duration: 2.4,
			Bubble:   b.murmur(EventInteractionSpamClick, MoodAnnoyed),
		}
	}

	isPetting := count >= 2
	if !isPetting && !b.canUse(MoodHappy) {
		return nil
	}
	b.startCooldown(MoodHappy, b.Mode.clickHappyCooldown())
	stateID, duration := "waving", 1.2
	if isPetting {
		stateID, duration = "jumping", 1.4
	}
	return &Decision{
		Mood:     MoodHappy,
		StateID:  stateID,
		Duration: duration,
		Bubble:   b.murmur(EventInteractionClick, MoodHappy),
	}
}

// HandleDrag reacts to the pet being carried around.
func (b *Brain) HandleDrag() *Decision {
	return &Decision{Mood: MoodHappy, Bubble: b.murmur(EventDrag, MoodHappy)}
}

// HandleMouseNear reacts to the pointer hovering close to the pet.
func (b *Brain) HandleMouseNear() *Decision {
	if b.Mode == AttentionFocus || b.ReduceMotion {
		return nil
	}
	if !b.canUse(MoodCurious) {
		return nil
	}
	b.startCooldown(MoodCurious, 5)
	return &Decision{
		Mood:     MoodCurious,
		StateID:  "waiting",
		Duration: 1.8,
		Bubble:   b.murmur(EventMouseNear, MoodCurious),
	}
}

// HandleUserReturned greets the user at most every 90 minutes.
func (b *Brain) HandleUserReturned() *Decision {
	current := b.now()
	if b.lastReturnGreetingAt != nil && current-*b.lastReturnGreetingAt < 90*60 {
		return nil
	}
	if b.Mode == AttentionFocus || !b.canUse(MoodHappy) {
		return nil
	}
	b.lastReturnGreetingAt = &current
	b.startCooldown(MoodHappy, b.Mode.clickHappyCooldown())
	return &Decision{
		Mood:     MoodHappy,
		StateID:  "waving",
		Duration: 1.4,
		Bubble:   b.murmur(EventUserReturned, MoodHappy),
	}
}

// HandleIdlePulse fires periodically; it settles long-running sessions and
// produces rare ambient murmurs within the mode's idle budget.
func (b *Brain) HandleIdlePulse() *Decision {
	current := b.now()

	if b.runningStartedAt != nil && !b.didSettleLongRunning && current-*b.runningStartedAt >= 5*60 {
		b.didSettleLongRunning = true
		b.activeCodexState = "waiting"
		return &Decision{
			Mood:    MoodFocused,
			StateID: "waiting",
			Bubble:  b.murmur(EventCodexLongRunning, MoodFocused),
		}
	}

	if b.Mode == AttentionFocus || b.ReduceMotion {
		return nil
	}
	if b.activeCodexState != "idle" && b.activeCodexState != "waiting" {
		return nil
	}
	if b.lastIdleActionAt != nil && current-*b.lastIdleActionAt < b.Mode.minIdleActionGap() {
		return nil
	}
	recent := b.idleActionTimes[:0]
	for _, at := range b.idleActionTimes {
		if current-at < 60 {
			recent = append(recent, at)
		}
	}
	b.idleActionTimes = recent
	if len(b.idleActionTimes) >= b.Mode.idleBudgetPerMinute() {
		return nil
	}
	b.idleActionTimes = append(b.idleActionTimes, current)
	b.lastIdleActionAt = &current

	bubble := ""
	if b.isLateNight() {
		bubble = b.murmur(EventLateNight, MoodSleepy)
	} else {
		bubble = b.murmur(EventAmbient, MoodCurious)
	}
	return &Decision{Mood: MoodCurious, StateID: "idle", Duration: 0.9, Bubble: bubble}
}

// MuteMurmursForToday silences murmurs until local midnight.
func (b *Brain) MuteMurmursForToday() {
	b.history.MuteForToday(b.dialogueNow())
	b.persistHistory()
}

// MuteMurmurs silences murmurs for the given number of seconds.
func (b *Brain) MuteMurmurs(seconds float64) {
	b.history.Mute(seconds, b.dialogueNow())
	b.persistHistory()
}

func (b *Brain) History() *History {
	return b.history
}

func (b *Brain) murmur(event MurmurEvent, mood Mood) string {
	if b.ReduceMotion && !event.IsImportantWorkflowEvent() {
		return ""
	}
	settings := NewSettings(b.BubbleModeV)
	if event.IsImportantWorkflowEvent() || event == EventInteractionSpamClick {
		settings.GlobalCooldownSeconds = 0
	}
	settings.AllowLateNight = event == EventLateNight
	line := b.engine.MaybeSpeak(event, mood, settings, b.history)
	if line == nil {
		return ""
	}
	b.persistHistory()
	return line.Text
}

func (b *Brain) persistHistory() {
	if b.onHistory != nil {
		b.onHistory(b.history)
	}
}

func (b *Brain) isLateNight() bool {
	hour := time.Unix(int64(b.dialogueNow()), 0).Local().Hour()
	return hour >= 22 || hour < 5
}

func (b *Brain) canUse(mood Mood) bool {
	until, ok := b.cooldownUntil[mood]
	return !ok || b.now() >= until
}

func (b *Brain) startCooldown(mood Mood, seconds float64) {
	b.cooldownUntil[mood] = b.now() + seconds
}
