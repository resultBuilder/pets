package overlayhost

import "testing"

func TestDragStateForDelta(t *testing.T) {
	if got := dragStateForDelta(8, "running-left"); got != "running-right" {
		t.Fatalf("right drag state = %q", got)
	}
	if got := dragStateForDelta(-8, "running-right"); got != "running-left" {
		t.Fatalf("left drag state = %q", got)
	}
	if got := dragStateForDelta(0, "running-left"); got != "running-left" {
		t.Fatalf("zero-delta drag state should preserve previous, got %q", got)
	}
	if got := dragStateForDelta(0, ""); got != "running-right" {
		t.Fatalf("zero-delta default drag state = %q", got)
	}
}
