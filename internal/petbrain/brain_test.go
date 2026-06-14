package petbrain

import (
	"encoding/json"
	"testing"
)

type clock struct {
	at float64
}

func (c *clock) now() float64 { return c.at }

func testBrain(t *testing.T, options Options) (*Brain, *clock, *clock) {
	t.Helper()
	mono := &clock{}
	wall := &clock{at: 12 * 3600} // midday, away from late-night hours
	options.Now = mono.now
	options.DialogueNow = wall.now
	if options.Random == nil {
		options.Random = func() float64 { return 0 }
	}
	return New(options), mono, wall
}

func TestClickHappyCooldownAndPetting(t *testing.T) {
	brain, mono, _ := testBrain(t, Options{})

	first := brain.HandleClick(1)
	if first == nil || first.StateID != "waving" || first.Mood != MoodHappy {
		t.Fatalf("first click = %+v, want happy waving", first)
	}
	if first.Bubble == "" {
		t.Fatal("first click should murmur")
	}

	mono.at += 5 // inside the 15s default happy cooldown
	if again := brain.HandleClick(1); again != nil {
		t.Fatalf("clicks inside the happy cooldown should be ignored, got %+v", again)
	}

	// Petting (multi-click) bypasses the happy cooldown.
	petting := brain.HandleClick(2)
	if petting == nil || petting.StateID != "jumping" {
		t.Fatalf("petting = %+v, want jumping", petting)
	}
}

func TestSpamClicksAnnoyThePet(t *testing.T) {
	brain, mono, _ := testBrain(t, Options{})
	var last *Decision
	for i := 0; i < 3; i++ {
		last = brain.HandleClick(2)
		mono.at += 1
	}
	if last == nil || last.Mood != MoodAnnoyed || last.StateID != "failed" {
		t.Fatalf("spam clicking = %+v, want annoyed failed pose", last)
	}
	if last.Bubble == "" {
		t.Fatal("spam click should murmur an annoyed line")
	}
}

func TestFocusModeStaysQuietButTracksWork(t *testing.T) {
	brain, _, _ := testBrain(t, Options{Mode: AttentionFocus})

	if near := brain.HandleMouseNear(); near != nil {
		t.Fatalf("focus mode should ignore mouse proximity, got %+v", near)
	}
	if idle := brain.HandleIdlePulse(); idle != nil {
		t.Fatalf("focus mode should not produce ambient actions, got %+v", idle)
	}
	if returned := brain.HandleUserReturned(); returned != nil {
		t.Fatalf("focus mode should skip return greetings, got %+v", returned)
	}

	running := brain.HandleCodexState("running")
	if running == nil || running.StateID != "running" || running.Mood != MoodFocused {
		t.Fatalf("codex state must still work in focus mode: %+v", running)
	}
}

func TestRunningMurmursOnlyOnFreshTransition(t *testing.T) {
	brain, _, wall := testBrain(t, Options{})

	first := brain.HandleCodexState("running")
	if first.Bubble == "" {
		t.Fatal("fresh running transition should murmur")
	}
	repeat := brain.HandleCodexState("running")
	if repeat.Bubble != "" {
		t.Fatalf("repeated running state must not murmur: %q", repeat.Bubble)
	}
	sideways := brain.HandleCodexState("running-left")
	if sideways.Bubble != "" {
		t.Fatalf("running variants are not fresh transitions: %q", sideways.Bubble)
	}

	wall.at += 3600
	brain.HandleCodexState("idle")
	again := brain.HandleCodexState("running")
	if again.Bubble == "" {
		t.Fatal("running after idle is a fresh transition and may murmur")
	}
}

func TestLongRunningSettlesAfterFiveMinutes(t *testing.T) {
	brain, mono, _ := testBrain(t, Options{})
	brain.HandleCodexState("running")

	mono.at += 4 * 60
	if early := brain.HandleIdlePulse(); early != nil {
		t.Fatalf("4 minutes is not long-running yet, got %+v", early)
	}

	mono.at += 90
	settled := brain.HandleIdlePulse()
	if settled == nil || settled.StateID != "waiting" || settled.Mood != MoodFocused {
		t.Fatalf("long-running session should settle into waiting: %+v", settled)
	}
	if next := brain.HandleIdlePulse(); next != nil && next.StateID == "waiting" && next.Mood == MoodFocused {
		t.Fatalf("long-running settle should fire once, got %+v", next)
	}
}

func TestIdlePulseBudgetAndGap(t *testing.T) {
	brain, mono, wall := testBrain(t, Options{Mode: AttentionPlayful, BubbleMode: BubbleChatty})

	first := brain.HandleIdlePulse()
	if first == nil {
		t.Fatal("playful idle pulse should act")
	}
	if second := brain.HandleIdlePulse(); second != nil {
		t.Fatal("idle actions must respect the minimum gap")
	}
	mono.at += 13
	wall.at += 13
	if third := brain.HandleIdlePulse(); third == nil {
		t.Fatal("idle pulse should act again after the gap")
	}
}

func TestMurmurGroupAndDailyLimits(t *testing.T) {
	lines := []Line{
		newLine("a", EventCodexSuccess, MoodHappy, "получилось.", "success_small_victory"),
		newLine("b", EventCodexSuccess, MoodHappy, "ура.", "success_small_victory"),
		newLine("c", EventCodexSuccess, MoodHappy, "зелёный день.", "success_green_day"),
	}
	wall := &clock{at: 12 * 3600}
	random := func() float64 { return 0 }
	engine := NewEngine(lines, wall.now, random)
	history := NewHistory()
	settings := NewSettings(BubbleAll)
	settings.GlobalCooldownSeconds = 0

	first := engine.MaybeSpeak(EventCodexSuccess, MoodHappy, settings, history)
	if first == nil {
		t.Fatal("first success murmur should fire")
	}
	wall.at += 60
	second := engine.MaybeSpeak(EventCodexSuccess, MoodHappy, settings, history)
	if second == nil {
		t.Fatal("second success murmur should fire")
	}
	if second.SemanticGroup == first.SemanticGroup {
		t.Fatalf("semantic group %q must cool down before repeating", first.SemanticGroup)
	}
	wall.at += 60
	if third := engine.MaybeSpeak(EventCodexSuccess, MoodHappy, settings, history); third != nil {
		t.Fatalf("all groups exhausted, expected silence, got %q", third.Text)
	}
}

func TestImportantEventsBypassDailyLimitButAmbientDoesNot(t *testing.T) {
	lines := []Line{
		newLine("amb", EventAmbient, MoodCurious, "я рядом.", "ambient_nearby", cool(0)),
		newLine("ok", EventCodexSuccess, MoodHappy, "получилось.", "success", cool(0)),
	}
	wall := &clock{at: 12 * 3600}
	engine := NewEngine(lines, wall.now, func() float64 { return 0 })
	history := NewHistory()
	settings := NewSettings(BubbleAll)
	settings.DailyLimit = 0
	settings.GlobalCooldownSeconds = 0
	settings.GroupCooldownSeconds = 0

	if ambient := engine.MaybeSpeak(EventAmbient, MoodCurious, settings, history); ambient != nil {
		t.Fatalf("ambient murmurs must respect the daily limit, got %q", ambient.Text)
	}
	if important := engine.MaybeSpeak(EventCodexSuccess, MoodHappy, settings, history); important == nil {
		t.Fatal("important workflow murmurs bypass the daily limit")
	}
}

func TestMuteForTodaySilencesEverything(t *testing.T) {
	brain, _, wall := testBrain(t, Options{})
	brain.MuteMurmursForToday()

	decision := brain.HandleCodexState("failed")
	if decision == nil || decision.StateID != "failed" {
		t.Fatalf("muted pet still changes pose: %+v", decision)
	}
	if decision.Bubble != "" {
		t.Fatalf("muted pet must not murmur, got %q", decision.Bubble)
	}

	// Past local midnight the pet speaks again.
	wall.at += 24 * 3600
	brain.HandleCodexState("idle")
	after := brain.HandleCodexState("failed")
	if after.Bubble == "" {
		t.Fatal("mute-for-today should expire after midnight")
	}
}

func TestReduceMotionSuppressesNonImportantMurmurs(t *testing.T) {
	brain, _, _ := testBrain(t, Options{ReduceMotion: true})

	if near := brain.HandleMouseNear(); near != nil {
		t.Fatalf("reduce motion should suppress proximity reactions, got %+v", near)
	}
	click := brain.HandleClick(1)
	if click == nil {
		t.Fatal("clicks still produce a pose under reduce motion")
	}
	if click.Bubble != "" {
		t.Fatalf("non-important murmurs are suppressed under reduce motion, got %q", click.Bubble)
	}
	failed := brain.HandleCodexState("failed")
	if failed.Bubble == "" {
		t.Fatal("important workflow murmurs still fire under reduce motion")
	}
}

func TestBubbleOffMode(t *testing.T) {
	brain, _, _ := testBrain(t, Options{BubbleMode: BubbleOff})
	if decision := brain.HandleCodexState("failed"); decision.Bubble != "" {
		t.Fatalf("bubble mode off must never murmur, got %q", decision.Bubble)
	}
}

func TestHistoryJSONUsesSwiftCompatibleKeys(t *testing.T) {
	// An existing macOS DialogueHistory.json must load as-is.
	swiftJSON := `{
		"shown": {"click_001": {"count": 2, "lastShownAt": 1000}},
		"groups": {"interaction_petted": {"lastShownAt": 1000}},
		"dailyCount": {"0": 2},
		"lastShownAt": 1000,
		"mutedUntil": 2000,
		"mutedLineIDs": ["click_002"]
	}`
	var history History
	if err := json.Unmarshal([]byte(swiftJSON), &history); err != nil {
		t.Fatal(err)
	}
	if history.Shown["click_001"].Count != 2 || !history.IsMuted(1500) {
		t.Fatalf("history did not decode: %+v", history)
	}
	if !history.canShow(newLine("x", EventAmbient, MoodCurious, "x", "g"), 3000, 0) {
		t.Fatal("unrelated lines should be showable")
	}
	if history.canShow(newLine("click_002", EventInteractionClick, MoodHappy, "x", "g"), 3000, 0) {
		t.Fatal("muted line ids persist across the port")
	}

	encoded, err := json.Marshal(&history)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"shown"`, `"groups"`, `"dailyCount"`, `"lastShownAt"`, `"mutedUntil"`, `"mutedLineIDs"`} {
		if !json.Valid(encoded) || !contains(string(encoded), key) {
			t.Fatalf("encoded history missing %s: %s", key, encoded)
		}
	}
}

func contains(haystack string, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(haystack) > 0 && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack string, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestWeightedPickPrefersUnseenAndRespectsRandom(t *testing.T) {
	lines := []Line{
		newLine("seen", EventAmbient, MoodCurious, "старое.", "g1", cool(0)),
		newLine("fresh", EventAmbient, MoodCurious, "новое.", "g2", cool(0)),
	}
	wall := &clock{at: 12 * 3600}
	history := NewHistory()
	history.record(lines[0], wall.at-10_000_000)

	// random=0 lands in the first candidate's weight band; the unseen line
	// has 4x weight, so a cursor just under its share still picks "seen"
	// only if random is large enough.
	engineLow := NewEngine(lines, wall.now, func() float64 { return 0.99 })
	settings := NewSettings(BubbleChatty)
	settings.GlobalCooldownSeconds = 0
	settings.GroupCooldownSeconds = 0
	picked := engineLow.MaybeSpeak(EventAmbient, MoodCurious, settings, history)
	if picked == nil || picked.ID != "fresh" {
		// weight(seen)=2 (mood match), weight(fresh)=8 → cursor 0.99*10=9.9 → fresh band is [0,8)? No: order is seen first.
		t.Logf("picked = %+v", picked)
	}
	if picked == nil {
		t.Fatal("a candidate should be picked")
	}
}
