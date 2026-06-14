package x11overlay

import (
	"image"

	"codex-pets/internal/render"
)

type pointerAction int

const (
	actionNone pointerAction = iota
	actionDrag
	actionUpdate
	actionBrowser
	actionApprove
	actionDeny
)

// routeButtonPress decides what a left click at (x, y) does. The update pill
// and approval buttons win over the pet body when they overlap.
func routeButtonPress(regions render.Regions, x int, y int) pointerAction {
	point := image.Pt(x, y)
	if regions.ApprovalID != "" {
		if point.In(regions.ApprovalApprove) {
			return actionApprove
		}
		if point.In(regions.ApprovalDeny) {
			return actionDeny
		}
	}
	if regions.UpdateAction != render.UpdateActionNone && point.In(regions.UpdatePill) {
		return actionUpdate
	}
	if point.In(regions.Body) {
		return actionDrag
	}
	return actionNone
}

func routeContextPress(regions render.Regions, x int, y int) pointerAction {
	if image.Pt(x, y).In(regions.Body) {
		return actionBrowser
	}
	return actionNone
}
