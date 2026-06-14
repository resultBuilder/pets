//go:build linux && cgo

package x11overlay

func Supported() bool {
	return true
}
