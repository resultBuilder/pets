package overlayhost

import (
	"image"

	"codex-pets/internal/render"
)

// EventKind classifies semantic backend events. Backends own raw pointer
// handling (hit-testing against the regions they were given, applying drag
// movement to their native surface) and report only what the host loop
// needs for pet behavior.
type EventKind int

const (
	// EventBodyPress: a press landed on the pet body (drag may follow).
	EventBodyPress EventKind = iota
	// EventDragStep: the backend moved the surface; DX carries the
	// horizontal delta so the host picks the run direction.
	EventDragStep
	// EventRelease ends a press; Moved reports whether it was a drag.
	EventRelease
	// EventPillPress: the update pill was clicked.
	EventPillPress
	// EventRedraw: the backend lost its contents and needs a re-present.
	EventRedraw
	// EventScaleChanged: the output scale changed; Scale carries the new
	// render scale and the host rebuilds its renderer.
	EventScaleChanged
)

type Event struct {
	Kind  EventKind
	DX    int
	DY    int
	Moved bool
	Scale float64
}

// Backend is a native windowing surface for the pet overlay.
type Backend interface {
	// Open creates the surface for a logical layout scaled by userScale
	// (0 = autodetect) and returns the initial render scale.
	Open(userScale float64) (renderScale float64, err error)
	// Present uploads a composed frame sized render.NewRenderer(scale).Size().
	Present(frame *image.NRGBA)
	// SetRegions updates interactive and painted areas (buffer pixels).
	SetRegions(regions render.Regions)
	// Pump translates pending native events; called on a fast tick.
	Pump() []Event
	Close()
}
