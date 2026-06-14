package x11overlay

import (
	"image"
	"testing"

	"codex-pets/internal/render"
)

func TestRouteButtonPress(t *testing.T) {
	regions := render.Regions{
		Body:            image.Rect(40, 90, 200, 270),
		UpdatePill:      image.Rect(60, 250, 180, 270),
		UpdateAction:    render.UpdateActionApply,
		ApprovalID:      "approval-1",
		ApprovalApprove: image.Rect(90, 120, 140, 142),
		ApprovalDeny:    image.Rect(148, 120, 198, 142),
	}

	if got := routeButtonPress(regions, 120, 150); got != actionDrag {
		t.Fatalf("body click = %v, want drag", got)
	}
	if got := routeButtonPress(regions, 120, 130); got != actionApprove {
		t.Fatalf("approve click = %v, want approve", got)
	}
	if got := routeButtonPress(regions, 160, 130); got != actionDeny {
		t.Fatalf("deny click = %v, want deny", got)
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

func TestRouteContextPress(t *testing.T) {
	regions := render.Regions{
		Body:       image.Rect(40, 90, 200, 270),
		UpdatePill: image.Rect(60, 250, 180, 270),
	}

	if got := routeContextPress(regions, 120, 150); got != actionBrowser {
		t.Fatalf("body right-click = %v, want browser", got)
	}
	if got := routeContextPress(regions, 5, 5); got != actionNone {
		t.Fatalf("outside right-click = %v, want none", got)
	}
}
