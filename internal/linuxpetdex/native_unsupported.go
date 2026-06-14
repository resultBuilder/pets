//go:build !linux || !cgo || (!webkitgtk41 && !webkitgtk40)

package linuxpetdex

import "context"

func openNative(context.Context, Options) error {
	return ErrUnsupported
}
