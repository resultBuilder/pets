//go:build !linux || !cgo

package x11overlay

import (
	"errors"

	"codex-pets/internal/overlayhost"
)

var ErrUnsupported = errors.New("Linux X11 overlay requires linux, cgo, and libX11/libXext development headers")

func New() (overlayhost.Backend, error) {
	return nil, ErrUnsupported
}

func Supported() bool {
	return false
}
