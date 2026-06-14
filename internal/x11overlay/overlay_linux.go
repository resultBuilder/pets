//go:build linux && cgo

package x11overlay

/*
#cgo linux LDFLAGS: -lX11 -lXext
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/extensions/shape.h>

typedef struct {
	Display* display;
	int screen;
	Window window;
	GC gc;
	Visual* visual;
	Colormap colormap;
	Pixmap pixmap;
	XImage* image;
	int depth;
	int argb;
	int has_shape;
	unsigned long red_mask;
	unsigned long green_mask;
	unsigned long blue_mask;
	unsigned long alpha_mask;
	int byte_order;
	int x;
	int y;
	int width;
	int height;
} PetX11;

typedef struct {
	int event_type;
	int x;
	int y;
	int x_root;
	int y_root;
	int state;
	unsigned int button;
} PetX11Event;

#define PET_EVENT_BUTTON_PRESS 1
#define PET_EVENT_BUTTON_RELEASE 2
#define PET_EVENT_MOTION 3
#define PET_EVENT_EXPOSE 4
#define PET_EVENT_VISIBILITY 5

// pet_x11_auto_scale derives a UI scale factor from the X screen DPI.
static double pet_x11_auto_scale(void) {
	Display* display = XOpenDisplay(NULL);
	if (!display) return 1.0;
	int screen = DefaultScreen(display);
	double widthPx = (double)DisplayWidth(display, screen);
	double widthMM = (double)DisplayWidthMM(display, screen);
	XCloseDisplay(display);
	if (widthMM <= 0 || widthPx <= 0) return 1.0;
	return (widthPx * 25.4 / widthMM) / 96.0;
}

static void pet_x11_free(PetX11* pet) {
	if (!pet) return;
	if (pet->image) XDestroyImage(pet->image);
	if (pet->pixmap) XFreePixmap(pet->display, pet->pixmap);
	if (pet->gc) XFreeGC(pet->display, pet->gc);
	if (pet->window) XDestroyWindow(pet->display, pet->window);
	if (pet->argb && pet->colormap) XFreeColormap(pet->display, pet->colormap);
	if (pet->display) XCloseDisplay(pet->display);
	free(pet);
}

static PetX11* pet_x11_open(int width, int height) {
	XInitThreads();
	Display* display = XOpenDisplay(NULL);
	if (!display) return NULL;
	int screen = DefaultScreen(display);
	Window root = RootWindow(display, screen);

	// A 32-bit ARGB visual gives true per-pixel transparency, but only a
	// compositor turns the alpha channel into actual screen blending.
	char cm_name[32];
	snprintf(cm_name, sizeof cm_name, "_NET_WM_CM_S%d", screen);
	int composited = XGetSelectionOwner(display, XInternAtom(display, cm_name, False)) != None;

	int argb = 0;
	Visual* visual = DefaultVisual(display, screen);
	int depth = DefaultDepth(display, screen);
	Colormap colormap = DefaultColormap(display, screen);
	XVisualInfo vinfo;
	if (composited && XMatchVisualInfo(display, screen, 32, TrueColor, &vinfo)) {
		argb = 1;
		visual = vinfo.visual;
		depth = vinfo.depth;
		colormap = XCreateColormap(display, root, visual, AllocNone);
	}

	XSetWindowAttributes attrs;
	memset(&attrs, 0, sizeof attrs);
	attrs.override_redirect = True;
	attrs.background_pixel = 0;
	attrs.border_pixel = 0;
	attrs.colormap = colormap;
	attrs.event_mask = ExposureMask | ButtonPressMask | ButtonReleaseMask | PointerMotionMask | VisibilityChangeMask;

	int x = DisplayWidth(display, screen) - width - 32;
	int y = DisplayHeight(display, screen) - height - 64;
	if (x < 0) x = 0;
	if (y < 0) y = 0;

	Window window = XCreateWindow(
		display, root, x, y, width, height, 0, depth, InputOutput, visual,
		CWOverrideRedirect | CWBackPixel | CWBorderPixel | CWColormap | CWEventMask, &attrs
	);
	XStoreName(display, window, "Pi Pet");

	PetX11* pet = (PetX11*)calloc(1, sizeof(PetX11));
	if (!pet) {
		XDestroyWindow(display, window);
		XCloseDisplay(display);
		return NULL;
	}
	pet->display = display;
	pet->screen = screen;
	pet->window = window;
	pet->visual = visual;
	pet->colormap = colormap;
	pet->depth = depth;
	pet->argb = argb;
	pet->red_mask = visual->red_mask;
	pet->green_mask = visual->green_mask;
	pet->blue_mask = visual->blue_mask;
	pet->alpha_mask = 0;
	if (argb) {
		pet->alpha_mask = ~(visual->red_mask | visual->green_mask | visual->blue_mask) & 0xffffffffUL;
	}
	pet->byte_order = ImageByteOrder(display);
	pet->x = x;
	pet->y = y;
	pet->width = width;
	pet->height = height;

	int shape_event = 0, shape_error = 0;
	pet->has_shape = XShapeQueryExtension(display, &shape_event, &shape_error) ? 1 : 0;

	pet->gc = XCreateGC(display, window, 0, NULL);
	pet->pixmap = XCreatePixmap(display, window, width, height, depth);
	pet->image = XCreateImage(display, visual, depth, ZPixmap, 0, NULL, width, height, 32, 0);
	if (!pet->image) {
		pet_x11_free(pet);
		return NULL;
	}
	pet->image->data = (char*)calloc(1, (size_t)pet->image->bytes_per_line * height);
	if (!pet->image->data) {
		pet_x11_free(pet);
		return NULL;
	}

	XMapRaised(display, window);
	XFlush(display);
	return pet;
}

static char* pet_x11_image_data(PetX11* pet) { return pet->image->data; }
static int pet_x11_image_bytes_per_line(PetX11* pet) { return pet->image->bytes_per_line; }
static int pet_x11_image_bits_per_pixel(PetX11* pet) { return pet->image->bits_per_pixel; }

// pet_x11_present uploads the frame into the server-side back buffer and
// copies it to the window in one operation, so no intermediate clear is ever
// visible (the old renderer cleared and redrew in place, which flickered).
static void pet_x11_present(PetX11* pet) {
	XPutImage(pet->display, pet->pixmap, pet->gc, pet->image, 0, 0, 0, 0, pet->width, pet->height);
	XCopyArea(pet->display, pet->pixmap, pet->window, pet->gc, 0, 0, pet->width, pet->height, 0, 0);
	XFlush(pet->display);
}

static void pet_x11_refresh(PetX11* pet) {
	XCopyArea(pet->display, pet->pixmap, pet->window, pet->gc, 0, 0, pet->width, pet->height, 0, 0);
	XFlush(pet->display);
}

static void pet_x11_set_input_shape(PetX11* pet, XRectangle* rects, int count) {
	if (!pet->has_shape) return;
	XShapeCombineRectangles(pet->display, pet->window, ShapeInput, 0, 0, rects, count, ShapeSet, Unsorted);
	XFlush(pet->display);
}

static void pet_x11_set_bounding_shape(PetX11* pet, XRectangle* rects, int count) {
	if (!pet->has_shape) return;
	XShapeCombineRectangles(pet->display, pet->window, ShapeBounding, 0, 0, rects, count, ShapeSet, Unsorted);
	XFlush(pet->display);
}

static void pet_x11_raise(PetX11* pet) {
	XRaiseWindow(pet->display, pet->window);
	XFlush(pet->display);
}

static void pet_x11_move_by(PetX11* pet, int dx, int dy) {
	pet->x += dx;
	pet->y += dy;
	XMoveWindow(pet->display, pet->window, pet->x, pet->y);
	XFlush(pet->display);
}

static int pet_x11_next_event(PetX11* pet, PetX11Event* out) {
	if (!pet || XPending(pet->display) <= 0) return 0;
	XEvent event;
	XNextEvent(pet->display, &event);
	memset(out, 0, sizeof(PetX11Event));
	switch (event.type) {
	case ButtonPress:
		out->event_type = PET_EVENT_BUTTON_PRESS;
		out->x = event.xbutton.x;
		out->y = event.xbutton.y;
		out->x_root = event.xbutton.x_root;
		out->y_root = event.xbutton.y_root;
		out->button = event.xbutton.button;
		return 1;
	case ButtonRelease:
		out->event_type = PET_EVENT_BUTTON_RELEASE;
		out->x = event.xbutton.x;
		out->y = event.xbutton.y;
		out->x_root = event.xbutton.x_root;
		out->y_root = event.xbutton.y_root;
		out->button = event.xbutton.button;
		return 1;
	case MotionNotify:
		out->event_type = PET_EVENT_MOTION;
		out->x = event.xmotion.x;
		out->y = event.xmotion.y;
		out->x_root = event.xmotion.x_root;
		out->y_root = event.xmotion.y_root;
		return 1;
	case Expose:
		out->event_type = PET_EVENT_EXPOSE;
		return 1;
	case VisibilityNotify:
		out->event_type = PET_EVENT_VISIBILITY;
		out->state = event.xvisibility.state;
		return 1;
	default:
		return 1;
	}
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"math"
	"time"
	"unsafe"

	"codex-pets/internal/overlayhost"
	"codex-pets/internal/render"
)

// Backend implements overlayhost.Backend on X11: ARGB window when a
// compositor is present (opaque shaped fallback otherwise), double-buffered
// presents, XShape input regions, and pointer translation.
type Backend struct {
	win       *window
	regions   render.Regions
	pressed   bool
	moved     bool
	lastRootX int
	lastRootY int
	lastRaise time.Time
}

func New() (overlayhost.Backend, error) {
	return &Backend{}, nil
}

func (b *Backend) Open(userScale float64) (float64, error) {
	scale := userScale
	if scale <= 0 {
		scale = autoScale()
	}
	width, height := render.NewRenderer(scale).Size()
	win, err := openWindow(width, height)
	if err != nil {
		return 0, err
	}
	b.win = win
	return scale, nil
}

func (b *Backend) Present(frame *image.NRGBA) {
	if b.win == nil {
		return
	}
	b.win.upload(frame)
	b.win.present()
}

func (b *Backend) SetRegions(regions render.Regions) {
	b.regions = regions
	if b.win != nil {
		b.win.applyShape(regions)
	}
}

func (b *Backend) Close() {
	if b.win != nil {
		b.win.close()
		b.win = nil
	}
}

// Pump drains pending X11 events into semantic overlay events. Drag
// movement is applied here (XMoveWindow by root deltas); raise-on-obscured
// stays internal.
func (b *Backend) Pump() []overlayhost.Event {
	if b.win == nil {
		return nil
	}
	var events []overlayhost.Event
	for {
		var event C.PetX11Event
		if C.pet_x11_next_event(b.win.pet, &event) == 0 {
			return events
		}
		switch event.event_type {
		case C.PET_EVENT_BUTTON_PRESS:
			if event.button != 1 {
				continue
			}
			switch routeButtonPress(b.regions, int(event.x), int(event.y)) {
			case actionUpdate:
				events = append(events, overlayhost.Event{Kind: overlayhost.EventPillPress})
			case actionDrag:
				b.pressed = true
				b.moved = false
				b.lastRootX = int(event.x_root)
				b.lastRootY = int(event.y_root)
				events = append(events, overlayhost.Event{Kind: overlayhost.EventBodyPress})
			}
		case C.PET_EVENT_MOTION:
			if !b.pressed {
				continue
			}
			dx := int(event.x_root) - b.lastRootX
			dy := int(event.y_root) - b.lastRootY
			if dx == 0 && dy == 0 {
				continue
			}
			b.moved = true
			b.lastRootX = int(event.x_root)
			b.lastRootY = int(event.y_root)
			b.win.moveBy(dx, dy)
			events = append(events, overlayhost.Event{Kind: overlayhost.EventDragStep, DX: dx, DY: dy})
		case C.PET_EVENT_BUTTON_RELEASE:
			if b.pressed && event.button == 1 {
				b.pressed = false
				events = append(events, overlayhost.Event{Kind: overlayhost.EventRelease, Moved: b.moved})
			}
		case C.PET_EVENT_EXPOSE:
			b.win.refresh()
		case C.PET_EVENT_VISIBILITY:
			if event.state != C.VisibilityUnobscured && time.Since(b.lastRaise) > time.Second {
				b.lastRaise = time.Now()
				b.win.raise()
			}
		}
	}
}

// window wraps the X11 connection plus the persistent upload buffer.
type window struct {
	pet           *C.PetX11
	data          []byte
	width         int
	height        int
	argb          bool
	bytesPerLine  int
	bytesPerPixel int
	byteOrder     int
	redMask       uint64
	greenMask     uint64
	blueMask      uint64
	alphaMask     uint64
}

// Opaque fallback background for servers without a compositor.
var fallbackBackground = [3]uint8{244, 246, 250}

func openWindow(width int, height int) (*window, error) {
	pet := C.pet_x11_open(C.int(width), C.int(height))
	if pet == nil {
		return nil, errors.New("could not open X11 display")
	}
	bytesPerLine := int(C.pet_x11_image_bytes_per_line(pet))
	bitsPerPixel := int(C.pet_x11_image_bits_per_pixel(pet))
	if bitsPerPixel < 16 {
		C.pet_x11_free(pet)
		return nil, fmt.Errorf("unsupported X11 pixel depth: %d bits per pixel", bitsPerPixel)
	}
	return &window{
		pet:           pet,
		data:          unsafe.Slice((*byte)(unsafe.Pointer(C.pet_x11_image_data(pet))), bytesPerLine*height),
		width:         width,
		height:        height,
		argb:          pet.argb != 0,
		bytesPerLine:  bytesPerLine,
		bytesPerPixel: bitsPerPixel / 8,
		byteOrder:     int(pet.byte_order),
		redMask:       uint64(pet.red_mask),
		greenMask:     uint64(pet.green_mask),
		blueMask:      uint64(pet.blue_mask),
		alphaMask:     uint64(pet.alpha_mask),
	}, nil
}

func (w *window) close() {
	C.pet_x11_free(w.pet)
	w.pet = nil
	w.data = nil
}

// upload converts the composed NRGBA frame into the server's pixel format:
// premultiplied ARGB when composited, flattened onto a solid background
// otherwise.
func (w *window) upload(img *image.NRGBA) {
	if img.Bounds().Dx() != w.width || img.Bounds().Dy() != w.height {
		return
	}
	for y := 0; y < w.height; y++ {
		srcRow := img.Pix[y*img.Stride : y*img.Stride+w.width*4]
		dstRow := y * w.bytesPerLine
		for x := 0; x < w.width; x++ {
			r := srcRow[x*4]
			g := srcRow[x*4+1]
			b := srcRow[x*4+2]
			a := srcRow[x*4+3]
			var pixel uint64
			if w.argb {
				pixel = componentPixel(premultiply(r, a), w.redMask) |
					componentPixel(premultiply(g, a), w.greenMask) |
					componentPixel(premultiply(b, a), w.blueMask) |
					componentPixel(a, w.alphaMask)
			} else {
				pixel = componentPixel(flatten(r, a, fallbackBackground[0]), w.redMask) |
					componentPixel(flatten(g, a, fallbackBackground[1]), w.greenMask) |
					componentPixel(flatten(b, a, fallbackBackground[2]), w.blueMask)
			}
			writePixel(w.data[dstRow+x*w.bytesPerPixel:], pixel, w.bytesPerPixel, w.byteOrder)
		}
	}
}

func (w *window) present() {
	C.pet_x11_present(w.pet)
}

func (w *window) refresh() {
	C.pet_x11_refresh(w.pet)
}

func (w *window) raise() {
	C.pet_x11_raise(w.pet)
}

func (w *window) moveBy(dx int, dy int) {
	C.pet_x11_move_by(w.pet, C.int(dx), C.int(dy))
}

// applyShape restricts clicks to the pet body and the update pill; without a
// compositor it also shapes the window outline to the painted areas so the
// rest of the canvas does not cover the desktop.
func (w *window) applyShape(regions render.Regions) {
	input := []image.Rectangle{regions.Body}
	if regions.UpdateAction != render.UpdateActionNone && !regions.UpdatePill.Empty() {
		input = append(input, regions.UpdatePill)
	}
	w.setShape(input, func(rects *C.XRectangle, count C.int) {
		C.pet_x11_set_input_shape(w.pet, rects, count)
	})
	if !w.argb {
		w.setShape(regions.Visible, func(rects *C.XRectangle, count C.int) {
			C.pet_x11_set_bounding_shape(w.pet, rects, count)
		})
	}
}

func (w *window) setShape(rects []image.Rectangle, apply func(*C.XRectangle, C.int)) {
	xrects := make([]C.XRectangle, 0, len(rects))
	for _, rect := range rects {
		if rect.Empty() {
			continue
		}
		xrects = append(xrects, C.XRectangle{
			x:      C.short(rect.Min.X),
			y:      C.short(rect.Min.Y),
			width:  C.ushort(rect.Dx()),
			height: C.ushort(rect.Dy()),
		})
	}
	if len(xrects) == 0 {
		return
	}
	apply(&xrects[0], C.int(len(xrects)))
}

// autoScale rounds the detected DPI factor to quarter steps within [1, 3].
func autoScale() float64 {
	scale := float64(C.pet_x11_auto_scale())
	scale = math.Round(scale*4) / 4
	if scale < 1 {
		return 1
	}
	if scale > 3 {
		return 3
	}
	return scale
}

func premultiply(value uint8, alpha uint8) uint8 {
	return uint8((uint16(value)*uint16(alpha) + 127) / 255)
}

func flatten(value uint8, alpha uint8, background uint8) uint8 {
	return uint8((uint16(value)*uint16(alpha) + uint16(background)*uint16(255-alpha) + 127) / 255)
}

func componentPixel(value uint8, mask uint64) uint64 {
	if mask == 0 {
		return 0
	}
	shift := trailingZeros(mask)
	maxValue := mask >> shift
	return ((uint64(value) * maxValue) / 255) << shift
}

func trailingZeros(value uint64) uint {
	var shift uint
	for value&1 == 0 {
		shift++
		value >>= 1
	}
	return shift
}

func writePixel(dst []byte, pixel uint64, bytesPerPixel int, byteOrder int) {
	if byteOrder == int(C.MSBFirst) {
		for i := 0; i < bytesPerPixel; i++ {
			dst[i] = byte(pixel >> uint((bytesPerPixel-1-i)*8))
		}
		return
	}
	for i := 0; i < bytesPerPixel; i++ {
		dst[i] = byte(pixel >> uint(i*8))
	}
}
