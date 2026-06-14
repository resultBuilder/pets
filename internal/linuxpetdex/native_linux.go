//go:build linux && cgo && (webkitgtk41 || webkitgtk40)

package linuxpetdex

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct PetdexWindow PetdexWindow;

extern int petdex_gtk_init(void);
extern PetdexWindow* petdex_window_new(uintptr_t handle, const char* init_script, const char* uri);
extern void petdex_window_eval(PetdexWindow* window, const char* script);
extern void petdex_window_close(PetdexWindow* window);
extern void petdex_window_free(PetdexWindow* window);
extern void petdex_gtk_main(void);
*/
import "C"

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

var nativeMu sync.Mutex

func openNative(ctx context.Context, options Options) error {
	nativeMu.Lock()
	defer nativeMu.Unlock()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if C.petdex_gtk_init() == 0 {
		return fmt.Errorf("%w: GTK could not be initialized", ErrUnsupported)
	}

	b, err := newBrowser(ctx, options)
	if err != nil {
		return err
	}
	defer b.close()

	handle := newBrowserHandle(b)
	defer handle.Delete()

	initScript := C.CString(b.initScript())
	defer C.free(unsafe.Pointer(initScript))
	uri := C.CString(b.pageURL())
	defer C.free(unsafe.Pointer(uri))

	window := C.petdex_window_new(C.uintptr_t(handle), initScript, uri)
	if window == nil {
		return fmt.Errorf("%w: WebKitGTK window could not be created", ErrUnsupported)
	}
	defer C.petdex_window_free(window)

	var alive atomic.Bool
	alive.Store(true)
	b.setEval(func(script string) {
		if !alive.Load() {
			return
		}
		cScript := C.CString(script)
		C.petdex_window_eval(window, cScript)
		C.free(unsafe.Pointer(cScript))
	})

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			C.petdex_window_close(window)
		case <-done:
		}
	}()

	C.petdex_gtk_main()
	alive.Store(false)
	b.clearEval()
	close(done)
	return nil
}
