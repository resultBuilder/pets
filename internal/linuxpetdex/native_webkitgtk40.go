//go:build linux && cgo && webkitgtk40

package linuxpetdex

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.0
#include "native_webkitgtk_impl.h"
*/
import "C"

var _ = C.int(0)
