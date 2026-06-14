// Package waylandoverlay renders the pet as a native Wayland layer-shell
// surface: true per-pixel transparency, always-above placement, and
// click-through come from first-class protocol features instead of X11
// workarounds. Pure Go — the wire client lives in internal/wayland.
package waylandoverlay

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"time"

	"codex-pets/internal/overlayhost"
	"codex-pets/internal/protocol"
	"codex-pets/internal/render"
	"codex-pets/internal/wayland"
)

const (
	marginRightDefault  = 32
	marginBottomDefault = 64
	bufferCount         = 2
)

type buffer struct {
	id     uint32
	offset int
	busy   bool
}

// Backend implements overlayhost.Backend on wlr-layer-shell.
type Backend struct {
	connect func() (*wayland.Client, error)
	client  *wayland.Client

	compositor uint32
	shm        uint32
	layerShell uint32
	seat       uint32
	surface    uint32
	layer      uint32
	pointer    uint32

	pool    *wayland.ShmPool
	buffers []buffer

	bufferScale  int
	userScale    float64
	width        int // buffer px
	height       int
	logicalW     int
	logicalH     int
	marginRight  float64
	marginBottom float64

	outputScales map[uint32]int
	maxScale     int

	configured  bool
	closed      bool
	regions     render.Regions
	pending     []overlayhost.Event
	pointerX    float64 // logical surface coords
	pointerY    float64
	pressed     bool
	moved       bool
	hasPointerC bool
}

func New() (overlayhost.Backend, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("WAYLAND_DISPLAY is not set")
	}
	return NewWithConnect(wayland.Connect), nil
}

// NewWithConnect injects the compositor connection (tests use a socketpair
// against a mock compositor).
func NewWithConnect(connect func() (*wayland.Client, error)) *Backend {
	return &Backend{
		connect:      connect,
		outputScales: map[uint32]int{},
		maxScale:     1,
	}
}

func (b *Backend) Open(userScale float64) (float64, error) {
	client, err := b.connect()
	if err != nil {
		return 0, err
	}
	b.client = client
	b.userScale = userScale
	conn := client.Conn

	if b.compositor, err = client.Bind("wl_compositor", 4, nil); err != nil {
		return 0, err
	}
	if b.shm, err = client.Bind("wl_shm", 1, nil); err != nil {
		return 0, err
	}
	if b.layerShell, err = client.Bind("zwlr_layer_shell_v1", 1, nil); err != nil {
		// No layer-shell (GNOME/Mutter): the caller falls back to XWayland.
		return 0, err
	}
	if seat, err := client.Bind("wl_seat", 5, b.seatEvent); err == nil {
		b.seat = seat
	}
	for _, global := range client.Globals {
		if global.Interface != "wl_output" {
			continue
		}
		version := global.Version
		if version > 2 {
			version = 2
		}
		var outputID uint32
		outputID, err = client.Bind("wl_output", version, func(opcode uint16, r *wayland.Reader) {
			if opcode == 3 { // scale
				b.outputScales[outputID] = int(r.Int())
				b.recomputeMaxScale()
			}
		})
		if err != nil {
			return 0, err
		}
	}
	if err := conn.RoundTrip(); err != nil {
		return 0, err
	}

	renderScale := b.userScale
	if renderScale <= 0 {
		renderScale = render.DefaultScale * float64(b.maxScale)
	}
	if renderScale < render.MinScale {
		renderScale = render.MinScale
	}
	if renderScale > render.MaxScale {
		renderScale = render.MaxScale
	}
	b.bufferScale = 1
	if renderScale == math.Trunc(renderScale) {
		b.bufferScale = int(renderScale)
	}
	b.width, b.height = render.NewRenderer(renderScale).Size()
	b.logicalW = b.width / b.bufferScale
	b.logicalH = b.height / b.bufferScale
	b.marginRight = marginRightDefault
	b.marginBottom = marginBottomDefault

	b.surface = conn.NewID(b.surfaceEvent)
	if err := conn.Request(b.compositor, 0, wayland.NewID(b.surface)); err != nil {
		return 0, err
	}
	b.layer = conn.NewID(b.layerEvent)
	if err := conn.Request(b.layerShell, 0,
		wayland.NewID(b.layer), wayland.Obj(b.surface), wayland.Obj(0),
		wayland.Uint(wayland.LayerTop), wayland.Str("codex-pets")); err != nil {
		return 0, err
	}
	_ = conn.Request(b.layer, 0, wayland.Uint(uint32(b.logicalW)), wayland.Uint(uint32(b.logicalH)))
	_ = conn.Request(b.layer, 1, wayland.Uint(wayland.AnchorBottom|wayland.AnchorRight))
	_ = conn.Request(b.layer, 3, wayland.Int(0), wayland.Int(int32(b.marginRight)), wayland.Int(int32(b.marginBottom)), wayland.Int(0))
	_ = conn.Request(b.layer, 4, wayland.Uint(0)) // no keyboard interactivity
	if b.bufferScale > 1 {
		_ = conn.Request(b.surface, 8, wayland.Int(int32(b.bufferScale)))
	}
	if err := conn.Request(b.surface, 6); err != nil { // commit
		return 0, err
	}
	if err := conn.RoundTrip(); err != nil {
		return 0, err
	}
	if !b.configured {
		// Some compositors deliver configure slightly later.
		deadline := time.Now().Add(2 * time.Second)
		for !b.configured && time.Now().Before(deadline) {
			if err := conn.Pump(time.Now().Add(100 * time.Millisecond)); err != nil {
				return 0, err
			}
		}
		if !b.configured {
			return 0, errors.New("layer surface was never configured")
		}
	}
	if err := b.allocateBuffers(); err != nil {
		return 0, err
	}
	return renderScale, nil
}

func (b *Backend) recomputeMaxScale() {
	maxScale := 1
	for _, scale := range b.outputScales {
		if scale > maxScale {
			maxScale = scale
		}
	}
	b.maxScale = maxScale
}

func (b *Backend) allocateBuffers() error {
	if b.pool != nil {
		for _, old := range b.buffers {
			_ = b.client.Conn.Request(old.id, 0) // wl_buffer.destroy
		}
		b.pool.Destroy()
		b.pool = nil
		b.buffers = nil
	}
	stride := b.width * 4
	size := stride * b.height * bufferCount
	pool, err := b.client.CreateShmPool(b.shm, size)
	if err != nil {
		return err
	}
	b.pool = pool
	for i := 0; i < bufferCount; i++ {
		index := i
		offset := i * stride * b.height
		id, err := pool.CreateBuffer(offset, b.width, b.height, stride, func() {
			b.buffers[index].busy = false
		})
		if err != nil {
			return err
		}
		b.buffers = append(b.buffers, buffer{id: id, offset: offset})
	}
	return nil
}

func (b *Backend) surfaceEvent(opcode uint16, r *wayland.Reader) {
	switch opcode {
	case 0: // enter(output)
		output := r.Uint()
		if b.userScale > 0 {
			return
		}
		if scale, ok := b.outputScales[output]; ok && scale != b.bufferScale && scale >= 1 {
			b.pending = append(b.pending, overlayhost.Event{
				Kind:  overlayhost.EventScaleChanged,
				Scale: float64(scale),
			})
		}
	}
}

func (b *Backend) layerEvent(opcode uint16, r *wayland.Reader) {
	switch opcode {
	case 0: // configure(serial, width, height)
		serial := r.Uint()
		_ = b.client.Conn.Request(b.layer, 6, wayland.Uint(serial)) // ack_configure
		b.configured = true
	case 1: // closed
		b.closed = true
	}
}

func (b *Backend) seatEvent(opcode uint16, r *wayland.Reader) {
	if opcode != 0 { // capabilities
		return
	}
	capabilities := r.Uint()
	if capabilities&1 == 0 || b.hasPointerC || b.seat == 0 {
		return
	}
	b.hasPointerC = true
	b.pointer = b.client.Conn.NewID(b.pointerEvent)
	_ = b.client.Conn.Request(b.seat, 0, wayland.NewID(b.pointer))
}

func (b *Backend) pointerEvent(opcode uint16, r *wayland.Reader) {
	switch opcode {
	case 0: // enter(serial, surface, sx, sy)
		_ = r.Uint()
		_ = r.Uint()
		b.pointerX = r.Fixed()
		b.pointerY = r.Fixed()
	case 1: // leave
		if b.pressed {
			b.pressed = false
			b.pending = append(b.pending, overlayhost.Event{Kind: overlayhost.EventRelease, Moved: b.moved})
		}
	case 2: // motion(time, sx, sy)
		_ = r.Uint()
		sx := r.Fixed()
		sy := r.Fixed()
		if b.pressed {
			dx := sx - b.pointerX
			dy := sy - b.pointerY
			if dx != 0 || dy != 0 {
				b.moved = true
				b.dragBy(dx, dy)
				b.pending = append(b.pending, overlayhost.Event{
					Kind: overlayhost.EventDragStep,
					DX:   int(math.Round(dx * float64(b.bufferScale))),
					DY:   int(math.Round(dy * float64(b.bufferScale))),
				})
			}
			return
		}
		b.pointerX = sx
		b.pointerY = sy
	case 3: // button(serial, time, button, state)
		_ = r.Uint()
		_ = r.Uint()
		button := r.Uint()
		state := r.Uint()
		if button == wayland.BtnRight {
			if state == 1 {
				b.handleContextPress()
			}
			return
		}
		if button != wayland.BtnLeft {
			return
		}
		if state == 1 {
			b.handlePress()
		} else if b.pressed {
			b.pressed = false
			b.pending = append(b.pending, overlayhost.Event{Kind: overlayhost.EventRelease, Moved: b.moved})
		}
	}
}

func (b *Backend) handleContextPress() {
	x := int(math.Round(b.pointerX * float64(b.bufferScale)))
	y := int(math.Round(b.pointerY * float64(b.bufferScale)))
	if image.Pt(x, y).In(b.regions.Body) {
		b.pending = append(b.pending, overlayhost.Event{Kind: overlayhost.EventPetBrowserRequest})
	}
}

func (b *Backend) handlePress() {
	x := int(math.Round(b.pointerX * float64(b.bufferScale)))
	y := int(math.Round(b.pointerY * float64(b.bufferScale)))
	point := image.Pt(x, y)
	if b.regions.ApprovalID != "" {
		if point.In(b.regions.ApprovalApprove) {
			b.pending = append(b.pending, overlayhost.Event{
				Kind:             overlayhost.EventApprovalDecision,
				ApprovalID:       b.regions.ApprovalID,
				ApprovalDecision: protocol.ApprovalApproved,
			})
			return
		}
		if point.In(b.regions.ApprovalDeny) {
			b.pending = append(b.pending, overlayhost.Event{
				Kind:             overlayhost.EventApprovalDecision,
				ApprovalID:       b.regions.ApprovalID,
				ApprovalDecision: protocol.ApprovalDenied,
			})
			return
		}
	}
	if b.regions.UpdateAction != render.UpdateActionNone && point.In(b.regions.UpdatePill) {
		b.pending = append(b.pending, overlayhost.Event{Kind: overlayhost.EventPillPress})
		return
	}
	if point.In(b.regions.Body) {
		b.pressed = true
		b.moved = false
		b.pending = append(b.pending, overlayhost.Event{Kind: overlayhost.EventBodyPress})
	}
}

// dragBy moves the anchored surface by adjusting its margins; the surface
// then shifts under the pointer, so deltas stay relative to the grab point.
func (b *Backend) dragBy(dx float64, dy float64) {
	b.marginRight -= dx
	b.marginBottom -= dy
	if b.marginRight < 0 {
		b.marginRight = 0
	}
	if b.marginBottom < 0 {
		b.marginBottom = 0
	}
	_ = b.client.Conn.Request(b.layer, 3,
		wayland.Int(0), wayland.Int(int32(math.Round(b.marginRight))),
		wayland.Int(int32(math.Round(b.marginBottom))), wayland.Int(0))
	_ = b.client.Conn.Request(b.surface, 6) // commit
}

func (b *Backend) Present(frame *image.NRGBA) {
	if b.client == nil {
		return
	}
	width, height := frame.Bounds().Dx(), frame.Bounds().Dy()
	if width != b.width || height != b.height {
		// Output scale changed: resize buffers, keep the logical footprint.
		b.width, b.height = width, height
		if b.userScale <= 0 {
			b.bufferScale = b.maxScale
		}
		if b.bufferScale < 1 {
			b.bufferScale = 1
		}
		b.logicalW = width / b.bufferScale
		b.logicalH = height / b.bufferScale
		conn := b.client.Conn
		_ = conn.Request(b.layer, 0, wayland.Uint(uint32(b.logicalW)), wayland.Uint(uint32(b.logicalH)))
		_ = conn.Request(b.surface, 8, wayland.Int(int32(b.bufferScale)))
		if err := b.allocateBuffers(); err != nil {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: wayland buffer realloc: %v\n", err)
			return
		}
	}

	target := -1
	for index := range b.buffers {
		if !b.buffers[index].busy {
			target = index
			break
		}
	}
	if target < 0 {
		target = 0 // overwrite; at pet frame rates this is harmless
	}
	buf := &b.buffers[target]
	stride := b.width * 4
	// wl_shm ARGB8888 is premultiplied little-endian: bytes B, G, R, A.
	for y := 0; y < height; y++ {
		src := frame.Pix[y*frame.Stride : y*frame.Stride+width*4]
		dst := b.pool.Data[buf.offset+y*stride : buf.offset+y*stride+width*4]
		for x := 0; x < width; x++ {
			r := src[x*4]
			g := src[x*4+1]
			bl := src[x*4+2]
			a := src[x*4+3]
			dst[x*4] = premultiply(bl, a)
			dst[x*4+1] = premultiply(g, a)
			dst[x*4+2] = premultiply(r, a)
			dst[x*4+3] = a
		}
	}
	buf.busy = true
	conn := b.client.Conn
	_ = conn.Request(b.surface, 1, wayland.Obj(buf.id), wayland.Int(0), wayland.Int(0)) // attach
	_ = conn.Request(b.surface, 2, wayland.Int(0), wayland.Int(0),
		wayland.Int(int32(b.logicalW)), wayland.Int(int32(b.logicalH))) // damage
	_ = conn.Request(b.surface, 6) // commit
}

func (b *Backend) SetRegions(regions render.Regions) {
	b.regions = regions
	if b.client == nil {
		return
	}
	conn := b.client.Conn
	region := conn.NewID(nil)
	if err := conn.Request(b.compositor, 1, wayland.NewID(region)); err != nil {
		return
	}
	addRect := func(rect image.Rectangle) {
		if rect.Empty() {
			return
		}
		scale := b.bufferScale
		_ = conn.Request(region, 1,
			wayland.Int(int32(rect.Min.X/scale)), wayland.Int(int32(rect.Min.Y/scale)),
			wayland.Int(int32(rect.Dx()/scale)), wayland.Int(int32(rect.Dy()/scale)))
	}
	addRect(regions.Body)
	if regions.UpdateAction != render.UpdateActionNone {
		addRect(regions.UpdatePill)
	}
	if regions.ApprovalID != "" {
		addRect(regions.ApprovalApprove)
		addRect(regions.ApprovalDeny)
	}
	_ = conn.Request(b.surface, 5, wayland.Obj(region)) // set_input_region
	_ = conn.Request(region, 0)                         // destroy (surface keeps a copy)
	_ = conn.Request(b.surface, 6)                      // commit
}

func (b *Backend) Pump() []overlayhost.Event {
	if b.client == nil {
		return nil
	}
	if err := b.client.Conn.Pump(time.Now().Add(time.Millisecond)); err != nil {
		fmt.Fprintf(os.Stderr, "pi-pet-overlay: wayland: %v\n", err)
	}
	if b.closed {
		fmt.Fprintln(os.Stderr, "pi-pet-overlay: layer surface closed by the compositor")
		b.closed = false
	}
	events := b.pending
	b.pending = nil
	return events
}

func (b *Backend) Close() {
	if b.client == nil {
		return
	}
	if b.pool != nil {
		b.pool.Destroy()
	}
	b.client.Conn.Close()
	b.client = nil
}

func premultiply(value uint8, alpha uint8) uint8 {
	return uint8((uint16(value)*uint16(alpha) + 127) / 255)
}
