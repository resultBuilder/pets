package x11overlay

import (
	"image"
	"testing"

	"codex-pets/internal/render"
)

func TestRouteButtonPress(t *testing.T) {
	regions := render.Regions{
		Body:         image.Rect(40, 90, 200, 270),
		UpdatePill:   image.Rect(60, 250, 180, 270),
		UpdateAction: render.UpdateActionApply,
	}

	if got := routeButtonPress(regions, 120, 150); got != actionDrag {
		t.Fatalf("body click = %v, want drag", got)
	}
	// Pill overlaps the body bottom; the pill must win.
	if got := routeButtonPress(regions, 120, 260); got != actionUpdate {
		t.Fatalf("pill click = %v, want update", got)
	}
	if got := routeButtonPress(regions, 5, 5); got != actionNone {
		t.Fatalf("outside click = %v, want none", got)
	}

	// A pill without an action (progress stages) is not clickable.
	regions.UpdateAction = render.UpdateActionNone
	if got := routeButtonPress(regions, 120, 260); got != actionDrag {
		t.Fatalf("inactive pill click = %v, want drag (falls through to body)", got)
	}
}
