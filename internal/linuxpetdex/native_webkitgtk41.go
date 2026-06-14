//go:build linux && cgo && webkitgtk41

package linuxpetdex

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include "native_webkitgtk_impl.h"
*/
import "C"

var _ = C.int(0)
