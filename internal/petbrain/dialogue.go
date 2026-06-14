package petbrain

import (
	"time"
)

// MurmurEvent identifies what prompted the pet to consider speaking.
type MurmurEvent string

const (
	EventInteractionClick     MurmurEvent = "interaction.click"
	EventInteractionSpamClick MurmurEvent = "interaction.spam_click"
	EventMouseNear            MurmurEvent = "mouse.near"
	EventDrag                 MurmurEvent = "interaction.drag"
	EventCodexRunning         MurmurEvent = "codex.running"
	EventCodexLongRunning     MurmurEvent = "codex.long_running"
	EventCodexWaiting         MurmurEvent = "codex.waiting"
	EventCodexReview          MurmurEvent = "codex.review"
	EventCodexSuccess         MurmurEvent = "codex.success"
	EventCodexFailed          MurmurEvent = "codex.failed"
	EventUserReturned         MurmurEvent = "user.returned"
	EventLateNight            MurmurEvent = "ambient.late_night"
	EventAmbient              MurmurEvent = "ambient"
)

// IsImportantWorkflowEvent reports whether the event reflects agent work the
// user should hear about even under quiet settings.
func (e MurmurEvent) IsImportantWorkflowEvent() bool {
	switch e {
	case EventCodexWaiting, EventCodexReview, EventCodexSuccess, EventCodexFailed:
		return true
	default:
		return false
	}
}

type Rarity string

const (
	RarityCommon    Rarity = "common"
	RarityRare      Rarity = "rare"
	RarityLegendary Rarity = "legendary"
)

func (r Rarity) weightMultiplier() float64 {
	switch r {
	case RarityRare:
		return 0.3
	case RarityLegendary:
		return 0.08
	default:
		return 1
	}
}

// Line is one phrase the pet can murmur.
type Line struct {
	ID                  string
	Text                string
	Triggers            []string
	Moods               []string
	SemanticGroup       string
	Rarity              Rarity
	MinDaysBeforeRepeat int
	CooldownMinutes     int
	Tones               []string
	RequiresInteraction bool
	MaxShowsTotal       int // 0 means unlimited
}

// LineHistory and friends persist as JSON with the exact field names the
// macOS app used, so an existing DialogueHistory.json carries over.
type LineHistory struct {
	Count       int     `json:"count"`
	LastShownAt float64 `json:"lastShownAt"`
}

type GroupHistory struct {
	LastShownAt float64 `json:"lastShownAt"`
}

type History struct {
	Shown        map[string]LineHistory  `json:"shown"`
	Groups       map[string]GroupHistory `json:"groups"`
	DailyCount   map[string]int          `json:"dailyCount"`
	LastShownAt  *float64                `json:"lastShownAt,omitempty"`
	MutedUntil   *float64                `json:"mutedUntil,omitempty"`
	MutedLineIDs []string                `json:"mutedLineIDs"`
}

func NewHistory() *History {
	return &History{
		Shown:        map[string]LineHistory{},
		Groups:       map[string]GroupHistory{},
		DailyCount:   map[string]int{},
		MutedLineIDs: []string{},
	}
}

func (h *History) ensureMaps() {
	if h.Shown == nil {
		h.Shown = map[string]LineHistory{}
	}
	if h.Groups == nil {
		h.Groups = map[string]GroupHistory{}
	}
	if h.DailyCount == nil {
		h.DailyCount = map[string]int{}
	}
}

func (h *History) dailyLimitReached(limit int, now float64) bool {
	if limit < 0 {
		return false
	}
	return h.DailyCount[dayKey(now)] >= limit
}

func (h *History) globalCooldownActive(seconds float64, now float64) bool {
	if seconds <= 0 || h.LastShownAt == nil {
		return false
	}
	return now-*h.LastShownAt < seconds
}

func (h *History) IsMuted(now float64) bool {
	return h.MutedUntil != nil && now < *h.MutedUntil
}

func (h *History) canShow(line Line, now float64, groupCooldownSeconds float64) bool {
	for _, muted := range h.MutedLineIDs {
		if muted == line.ID {
			return false
		}
	}
	lineHistory, seen := h.Shown[line.ID]
	if line.MaxShowsTotal > 0 && seen && lineHistory.Count >= line.MaxShowsTotal {
		return false
	}
	if seen {
		exactRepeatSeconds := float64(max(0, line.MinDaysBeforeRepeat)) * 24 * 60 * 60
		lineCooldownSeconds := float64(max(0, line.CooldownMinutes)) * 60
		if exactRepeatSeconds > 0 && now-lineHistory.LastShownAt < exactRepeatSeconds {
			return false
		}
		if lineCooldownSeconds > 0 && now-lineHistory.LastShownAt < lineCooldownSeconds {
			return false
		}
	}
	if groupHistory, ok := h.Groups[line.SemanticGroup]; ok {
		lineGroupCooldown := float64(max(0, line.CooldownMinutes)) * 60
		effective := groupCooldownSeconds
		if lineGroupCooldown > effective {
			effective = lineGroupCooldown
		}
		if effective > 0 && now-groupHistory.LastShownAt < effective {
			return false
		}
	}
	return true
}

func (h *History) record(line Line, now float64) {
	h.ensureMaps()
	existing := h.Shown[line.ID]
	h.Shown[line.ID] = LineHistory{Count: existing.Count + 1, LastShownAt: now}
	h.Groups[line.SemanticGroup] = GroupHistory{LastShownAt: now}
	h.DailyCount[dayKey(now)]++
	at := now
	h.LastShownAt = &at
}

func (h *History) Mute(seconds float64, now float64) {
	if seconds < 0 {
		seconds = 0
	}
	next := now + seconds
	if h.MutedUntil == nil || next > *h.MutedUntil {
		h.MutedUntil = &next
	}
}

// MuteForToday silences murmurs until local midnight.
func (h *History) MuteForToday(now float64) {
	t := time.Unix(int64(now), 0).Local()
	endOfDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	until := float64(endOfDay.Unix())
	h.MutedUntil = &until
}

func dayKey(timestamp float64) string {
	day := int64(timestamp / 86_400)
	return intToString(day)
}

func intToString(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// Settings tune how often the pet is allowed to murmur.
type Settings struct {
	Mode                  BubbleMode
	EnabledTones          map[string]bool
	DailyLimit            int
	GlobalCooldownSeconds float64
	GroupCooldownSeconds  float64
	AllowLateNight        bool
}

// NewSettings fills mode-derived defaults the same way the Swift dialogue
// settings did; negative overrides mean "use the mode default".
func NewSettings(mode BubbleMode) Settings {
	return Settings{
		Mode:                  mode,
		EnabledTones:          map[string]bool{"soft": true, "coding": true},
		DailyLimit:            mode.defaultDailyMurmurLimit(),
		GlobalCooldownSeconds: mode.defaultGlobalMurmurCooldown(),
		GroupCooldownSeconds:  mode.defaultSemanticGroupCooldown(),
	}
}

func (s Settings) allows(line Line, event MurmurEvent) bool {
	if s.Mode == BubbleOff {
		return false
	}
	if event == EventLateNight && !s.AllowLateNight {
		return false
	}
	if s.Mode == BubbleImportantOnly && !event.IsImportantWorkflowEvent() {
		return false
	}
	hasEnabledTone := len(line.Tones) == 0
	for _, tone := range line.Tones {
		if s.EnabledTones[tone] {
			hasEnabledTone = true
		}
		if s.Mode == BubbleAll && tone == "chaotic" {
			return false
		}
	}
	return hasEnabledTone
}

// Engine picks murmur lines with rarity weighting and repeat protection.
type Engine struct {
	Lines  []Line
	now    func() float64
	random func() float64
}

func NewEngine(lines []Line, now func() float64, random func() float64) *Engine {
	return &Engine{Lines: lines, now: now, random: random}
}

// MaybeSpeak returns a line for the event or nil when the pet stays quiet.
func (e *Engine) MaybeSpeak(event MurmurEvent, mood Mood, settings Settings, history *History) *Line {
	current := e.now()
	if history.IsMuted(current) {
		return nil
	}
	if !event.IsImportantWorkflowEvent() && history.dailyLimitReached(settings.DailyLimit, current) {
		return nil
	}
	if history.globalCooldownActive(settings.GlobalCooldownSeconds, current) {
		return nil
	}

	candidates := []Line{}
	for _, line := range e.Lines {
		if !containsString(line.Triggers, string(event)) {
			continue
		}
		if len(line.Moods) > 0 && !containsString(line.Moods, string(mood)) {
			continue
		}
		if !settings.allows(line, event) {
			continue
		}
		if !history.canShow(line, current, settings.GroupCooldownSeconds) {
			continue
		}
		candidates = append(candidates, line)
	}

	selected := e.weightedPick(candidates, mood, history)
	if selected == nil {
		return nil
	}
	history.record(*selected, current)
	return selected
}

func (e *Engine) weightedPick(candidates []Line, mood Mood, history *History) *Line {
	if len(candidates) == 0 {
		return nil
	}
	weights := make([]float64, len(candidates))
	total := 0.0
	for index, line := range candidates {
		weight := line.Rarity.weightMultiplier()
		if _, seen := history.Shown[line.ID]; !seen {
			weight *= 4
		}
		if containsString(line.Moods, string(mood)) {
			weight *= 2
		}
		if weight < 0.001 {
			weight = 0.001
		}
		weights[index] = weight
		total += weight
	}
	if total <= 0 {
		return nil
	}
	cursor := e.random()
	if cursor < 0 {
		cursor = 0
	}
	if cursor > 0.999999 {
		cursor = 0.999999
	}
	cursor *= total
	for index, weight := range weights {
		if cursor < weight {
			return &candidates[index]
		}
		cursor -= weight
	}
	return &candidates[len(candidates)-1]
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
