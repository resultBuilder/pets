package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-pets/internal/protocol"
)

func writeTestPet(t *testing.T, frameWidth int, frameHeight int, columns int, rows int) string {
	t.Helper()
	dir := t.TempDir()
	atlas := image.NewNRGBA(image.Rect(0, 0, frameWidth*columns, frameHeight*rows))
	for row := 0; row < rows; row++ {
		for col := 0; col < columns; col++ {
			fill := color.NRGBA{R: uint8(row * 20), G: uint8(col * 25), B: uint8(row + col), A: 255}
			for y := row * frameHeight; y < (row+1)*frameHeight; y++ {
				for x := col * frameWidth; x < (col+1)*frameWidth; x++ {
					atlas.SetNRGBA(x, y, fill)
				}
			}
		}
	}
	file, err := os.Create(filepath.Join(dir, "spritesheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, atlas); err != nil {
		t.Fatal(err)
	}
	file.Close()
	manifest := `{"slug": "test-pet", "displayName": "Test Pet", "spritesheetPath": "spritesheet.png",
		"frameWidth": ` + itoa(frameWidth) + `, "frameHeight": ` + itoa(frameHeight) + `, "license": "MIT"}`
	if err := os.WriteFile(filepath.Join(dir, "pet.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func itoa(value int) string {
	digits := "0123456789"
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(digits[value%10]) + out
		value /= 10
	}
	return out
}

func TestStateTableMatchesMacHost(t *testing.T) {
	expected := []AnimationState{
		{"idle", 0, 6, 160 * time.Millisecond},
		{"running-right", 1, 8, 120 * time.Millisecond},
		{"running-left", 2, 8, 120 * time.Millisecond},
		{"waving", 3, 4, 140 * time.Millisecond},
		{"jumping", 4, 5, 140 * time.Millisecond},
		{"failed", 5, 8, 140 * time.Millisecond},
		{"waiting", 6, 6, 150 * time.Millisecond},
		{"running", 7, 6, 120 * time.Millisecond},
		{"review", 8, 6, 150 * time.Millisecond},
	}
	for _, want := range expected {
		if got := StateByID(want.ID); got != want {
			t.Fatalf("StateByID(%q) = %+v, want %+v", want.ID, got, want)
		}
	}
	if got := StateByID("unknown"); got.ID != "idle" {
		t.Fatalf("unknown state should fall back to idle, got %+v", got)
	}
}

func TestSpriteSheetFrameSelectionAndClamping(t *testing.T) {
	dir := writeTestPet(t, 4, 3, 8, 9)
	sheet, err := LoadSpriteSheet(dir)
	if err != nil {
		t.Fatal(err)
	}

	frame := sheet.Frame("waiting", 2, 40, 30)
	if frame.Bounds().Dx() != 40 || frame.Bounds().Dy() != 30 {
		t.Fatalf("frame bounds = %v", frame.Bounds())
	}
	// waiting is row 6, frame 2 → fill {R:120, G:50, B:8}.
	if got := frame.NRGBAAt(20, 15); got != (color.NRGBA{R: 120, G: 50, B: 8, A: 255}) {
		t.Fatalf("waiting frame sample = %+v", got)
	}

	// Index wraps within the state frame count (waving has 4 frames).
	wrapped := sheet.Frame("waving", 5, 40, 30)
	if got := wrapped.NRGBAAt(20, 15); got != (color.NRGBA{R: 60, G: 25, B: 4, A: 255}) {
		t.Fatalf("wrapped waving frame sample = %+v", got)
	}

	if cached := sheet.Frame("waiting", 2, 40, 30); cached != sheet.Frame("waiting", 2, 40, 30) {
		t.Fatal("identical frame requests should hit the cache")
	}
}

func TestNarrowAtlasNeverProducesPartialFrames(t *testing.T) {
	// Only 3 columns while running-right expects 8 frames: indexes must wrap
	// inside the 3 real columns instead of stretching a partial slice.
	dir := writeTestPet(t, 4, 3, 3, 2)
	sheet, err := LoadSpriteSheet(dir)
	if err != nil {
		t.Fatal(err)
	}
	frame := sheet.Frame("running-right", 7, 40, 30)
	// 7 % 3 = 1 → row 1, column 1 fill {R:20, G:25, B:2}.
	if got := frame.NRGBAAt(20, 15); got != (color.NRGBA{R: 20, G: 25, B: 2, A: 255}) {
		t.Fatalf("clamped frame sample = %+v", got)
	}
	// Row 6 does not exist in a 2-row atlas → falls back to row 0.
	fallback := sheet.Frame("waiting", 0, 40, 30)
	if got := fallback.NRGBAAt(20, 15); got != (color.NRGBA{R: 0, G: 0, B: 0, A: 255}) {
		t.Fatalf("row fallback sample = %+v", got)
	}
}

func TestLoadSpriteSheetDecodesPrebundledWebP(t *testing.T) {
	dir := filepath.Join("..", "..", "prebundled-pets", "pets", "boba")
	if _, err := os.Stat(filepath.Join(dir, "pet.json")); err != nil {
		t.Skipf("prebundled pets unavailable: %v", err)
	}
	sheet, err := LoadSpriteSheet(dir)
	if err != nil {
		t.Fatalf("WebP spritesheet must decode on Linux: %v", err)
	}
	frame := sheet.Frame("idle", 0, spriteLogicalWidth, spriteLogicalHeight)
	opaque := 0
	for offset := 3; offset < len(frame.Pix); offset += 4 {
		if frame.Pix[offset] > 0 {
			opaque++
		}
	}
	if opaque == 0 {
		t.Fatal("decoded WebP frame is fully transparent")
	}
}

func TestWrapTextIsRuneSafe(t *testing.T) {
	face := newFace(12)
	if face == nil {
		t.Fatal("embedded font failed to load")
	}
	lines := wrapText(face, "Pi выполняет очень-очень-длинное-слово-без-пробелов сейчас", 120, 3)
	if len(lines) == 0 {
		t.Fatal("expected wrapped lines")
	}
	for _, line := range lines {
		if !utf8Valid(line) {
			t.Fatalf("line %q is not valid UTF-8", line)
		}
		if textWidth(face, line) > 120 {
			t.Fatalf("line %q exceeds max width", line)
		}
	}

	overflow := wrapText(face, strings.Repeat("слово ", 40), 100, 2)
	if len(overflow) != 2 {
		t.Fatalf("expected 2 clamped lines, got %d", len(overflow))
	}
	if !strings.HasSuffix(overflow[1], "…") {
		t.Fatalf("clamped text should be ellipsized, got %q", overflow[1])
	}
}

func utf8Valid(value string) bool {
	for _, r := range value {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestComposeFullFrame(t *testing.T) {
	dir := writeTestPet(t, 4, 3, 8, 9)
	sheet, err := LoadSpriteSheet(dir)
	if err != nil {
		t.Fatal(err)
	}
	renderer := NewRenderer(1)
	renderer.SetSheet(sheet)

	spriteFrame, spriteRegions := renderer.Compose(Input{
		StateID:    "running",
		FrameIndex: 1,
	})
	// Sprite center shows the running row (row 7, col 1 -> {140,25,8}).
	spriteOnly := spriteRegions.Visible[0]
	center := spriteFrame.NRGBAAt((spriteOnly.Min.X+spriteOnly.Max.X)/2, (spriteOnly.Min.Y+spriteOnly.Max.Y)/2)
	if center != (color.NRGBA{R: 140, G: 25, B: 8, A: 255}) {
		t.Fatalf("sprite center = %+v", center)
	}

	frame, regions := renderer.Compose(Input{
		StateID:        "running",
		Bubble:         "Pi выполняет: сборка проекта",
		ActiveSessions: 2,
		Update:         &protocol.UpdateState{Available: true, CommitsBehind: 3},
		FrameIndex:     1,
	})

	width, height := renderer.Size()
	if frame.Bounds().Dx() != width || frame.Bounds().Dy() != height {
		t.Fatalf("frame bounds = %v", frame.Bounds())
	}
	// Window corners stay transparent for the compositor.
	if alpha := frame.NRGBAAt(0, height-1).A; alpha != 0 {
		t.Fatalf("corner alpha = %d, want 0", alpha)
	}
	sprite := regions.Visible[0]
	// Bubble renders an opaque chip near the top.
	if alpha := frame.NRGBAAt(width/2, 14).A; alpha == 0 {
		t.Fatal("bubble chip is missing")
	}
	if regions.UpdateAction != UpdateActionApply || regions.UpdatePill.Empty() {
		t.Fatalf("update pill regions = %+v", regions)
	}
	pillCenter := frame.NRGBAAt((regions.UpdatePill.Min.X+regions.UpdatePill.Max.X)/2, (regions.UpdatePill.Min.Y+regions.UpdatePill.Max.Y)/2)
	if pillCenter.A == 0 {
		t.Fatal("update pill is not drawn")
	}
	if !regions.Body.Overlaps(sprite) {
		t.Fatalf("body region %v does not cover the sprite", regions.Body)
	}

	// Without a sheet the placeholder card still renders and stays draggable.
	empty := NewRenderer(2)
	placeholder, placeholderRegions := empty.Compose(Input{StateID: "idle"})
	if placeholderRegions.Body.Empty() {
		t.Fatal("placeholder body region is empty")
	}
	card := placeholder.NRGBAAt((placeholderRegions.Body.Min.X+placeholderRegions.Body.Max.X)/2, (placeholderRegions.Body.Min.Y+placeholderRegions.Body.Max.Y)/2)
	if card.A == 0 {
		t.Fatal("placeholder card is not drawn")
	}
}

func TestComposeApprovalButtons(t *testing.T) {
	renderer := NewRenderer(1)
	frame, regions := renderer.Compose(Input{
		StateID:           "waiting",
		Bubble:            "Approval needed: git push origin main",
		PendingApprovalID: "approval-1",
	})

	if regions.ApprovalID != "approval-1" {
		t.Fatalf("approval id = %q, want approval-1", regions.ApprovalID)
	}
	if regions.ApprovalApprove.Empty() || regions.ApprovalDeny.Empty() {
		t.Fatalf("approval button regions are empty: %+v", regions)
	}
	if len(regions.Visible) < 2 {
		t.Fatalf("visible regions = %v, want sprite and bubble", regions.Visible)
	}
	bubble := regions.Visible[1]
	if !regions.ApprovalApprove.In(bubble) || !regions.ApprovalDeny.In(bubble) {
		t.Fatalf("buttons %v %v should be inside bubble %v", regions.ApprovalApprove, regions.ApprovalDeny, bubble)
	}
	approveCenter := image.Pt(
		(regions.ApprovalApprove.Min.X+regions.ApprovalApprove.Max.X)/2,
		(regions.ApprovalApprove.Min.Y+regions.ApprovalApprove.Max.Y)/2,
	)
	denyCenter := image.Pt(
		(regions.ApprovalDeny.Min.X+regions.ApprovalDeny.Max.X)/2,
		(regions.ApprovalDeny.Min.Y+regions.ApprovalDeny.Max.Y)/2,
	)
	if alpha := frame.NRGBAAt(approveCenter.X, approveCenter.Y).A; alpha == 0 {
		t.Fatal("approve button is not drawn")
	}
	if alpha := frame.NRGBAAt(denyCenter.X, denyCenter.Y).A; alpha == 0 {
		t.Fatal("deny button is not drawn")
	}
}

func TestUpdatePillStages(t *testing.T) {
	cases := []struct {
		state  *protocol.UpdateState
		text   string
		action UpdateAction
	}{
		{nil, "", UpdateActionNone},
		{&protocol.UpdateState{}, "", UpdateActionNone},
		{&protocol.UpdateState{Stage: "checking"}, "Checking for updates…", UpdateActionNone},
		{&protocol.UpdateState{Available: true, CommitsBehind: 2}, "Update available (2) — click", UpdateActionApply},
		{&protocol.UpdateState{Available: true}, "Rebuild available — click", UpdateActionApply},
		{&protocol.UpdateState{Stage: "building"}, "Building…", UpdateActionNone},
		{&protocol.UpdateState{Stage: "restartPending"}, "Restarting…", UpdateActionNone},
		{&protocol.UpdateState{Stage: "failed", Message: "boom"}, "Update failed — click to dismiss", UpdateActionDismiss},
	}
	for _, c := range cases {
		text, action, _ := updatePill(c.state)
		if text != c.text || action != c.action {
			t.Fatalf("updatePill(%+v) = (%q, %q), want (%q, %q)", c.state, text, action, c.text, c.action)
		}
	}
}
