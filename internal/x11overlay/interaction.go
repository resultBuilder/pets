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
)

// routeButtonPress decides what a left click at (x, y) does. The update pill
// wins over the pet body when they overlap.
func routeButtonPress(regions render.Regions, x int, y int) pointerAction {
	point := image.Pt(x, y)
	if regions.UpdateAction != render.UpdateActionNone && point.In(regions.UpdatePill) {
		return actionUpdate
	}
	if point.In(regions.Body) {
		return actionDrag
	}
	return actionNone
}
