//go:build linux && cgo && (webkitgtk41 || webkitgtk40)

package linuxpetdex

/*
#include <stdint.h>
#include <stdlib.h>
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"
)

func newBrowserHandle(b *browser) cgo.Handle {
	return cgo.NewHandle(b)
}

func browserFromHandle(handle C.uintptr_t) *browser {
	value := cgo.Handle(handle).Value()
	b, _ := value.(*browser)
	return b
}

//export codexPetsLinuxBridgeMessage
func codexPetsLinuxBridgeMessage(handle C.uintptr_t, raw *C.char) {
	b := browserFromHandle(handle)
	if b == nil || raw == nil {
		return
	}
	message := C.GoString(raw)
	go b.handleMessage(message)
}

//export codexPetsLinuxLoaded
func codexPetsLinuxLoaded(handle C.uintptr_t) {
	b := browserFromHandle(handle)
	if b == nil {
		return
	}
	go b.onLoaded()
}

//export codexPetsLinuxClosed
func codexPetsLinuxClosed(handle C.uintptr_t) {
	_ = handle
}

func freeCString(ptr *C.char) {
	C.free(unsafe.Pointer(ptr))
}
