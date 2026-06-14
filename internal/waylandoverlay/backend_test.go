package waylandoverlay

import (
	"image"
	"testing"
	"time"

	"codex-pets/internal/overlayhost"
	"codex-pets/internal/protocol"
	"codex-pets/internal/render"
	"codex-pets/internal/wayland"
	"codex-pets/internal/wayland/wltest"
)

func pumpUntil(t *testing.T, backend *Backend, what string, predicate func([]overlayhost.Event) bool) []overlayhost.Event {
	t.Helper()
	collected := []overlayhost.Event{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		collected = append(collected, backend.Pump()...)
		if predicate(collected) {
			return collected
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %+v", what, collected)
	return nil
}

func waitFor(t *testing.T, what string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBackendOpensPresentsAndTranslatesPointer(t *testing.T) {
	clientEnd, serverEnd := wltest.SocketPair(t)
	defer clientEnd.Close()
	defer serverEnd.Close()
	mock := wltest.NewCompositor(t, serverEnd)
	go mock.Serve()

	backend := NewWithConnect(func() (*wayland.Client, error) {
		return wayland.Handshake(wayland.NewConn(clientEnd))
	})
	scale, err := backend.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if scale != render.DefaultScale {
		t.Fatalf("render scale = %v, want %v (mock output scale 1)", scale, render.DefaultScale)
	}
	if margins := mock.Margins(); margins != [4]int32{0, 32, 64, 0} {
		t.Fatalf("initial margins = %v", margins)
	}

	// Present a real composed frame: the pool fd must reach the compositor
	// and the surface must be committed with content.
	renderer := render.NewRenderer(scale)
	frame, regions := renderer.Compose(render.Input{StateID: "idle"})
	backend.Present(frame)
	backend.SetRegions(regions)
	if err := backend.client.Conn.RoundTrip(); err != nil {
		t.Fatal(err)
	}
	if fds := mock.FDs(); len(fds) != 1 {
		t.Fatalf("compositor received %d pool fds, want 1", len(fds))
	}
	waitFor(t, "commits", func() bool { return mock.Commits() >= 2 })
	waitFor(t, "input region", func() bool { return len(mock.InputRects()) >= 1 })
	rects := mock.InputRects()
	if rects[0][2] <= 0 || rects[0][3] <= 0 {
		t.Fatalf("input rect is empty: %v", rects)
	}

	// Pointer: press on the body, drag right, release.
	waitFor(t, "pointer object", func() bool { return mock.BoundID("__pointer") != 0 })
	pointer := mock.BoundID("__pointer")
	surface := mock.BoundID("__surface")
	bodyX := float64((regions.Body.Min.X + regions.Body.Max.X) / 2)
	bodyY := float64((regions.Body.Min.Y + regions.Body.Max.Y) / 2)

	mock.SendEvent(pointer, 0, wayland.Uint(1), wayland.Obj(surface), wayland.Fixed(bodyX), wayland.Fixed(bodyY))
	mock.SendEvent(pointer, 3, wayland.Uint(2), wayland.Uint(0), wayland.Uint(wayland.BtnLeft), wayland.Uint(1))
	events := pumpUntil(t, backend, "body press", func(events []overlayhost.Event) bool {
		return len(events) >= 1
	})
	if events[0].Kind != overlayhost.EventBodyPress {
		t.Fatalf("first event = %+v, want body press", events[0])
	}

	mock.SendEvent(pointer, 2, wayland.Uint(0), wayland.Fixed(bodyX+10), wayland.Fixed(bodyY))
	events = pumpUntil(t, backend, "drag step", func(events []overlayhost.Event) bool {
		return len(events) >= 1
	})
	if events[0].Kind != overlayhost.EventDragStep || events[0].DX <= 0 {
		t.Fatalf("drag event = %+v, want rightward drag step", events[0])
	}
	// Dragging right shrinks the right margin (anchored bottom-right).
	waitFor(t, "margins to move", func() bool { return mock.Margins()[1] < 32 })

	mock.SendEvent(pointer, 3, wayland.Uint(3), wayland.Uint(0), wayland.Uint(wayland.BtnLeft), wayland.Uint(0))
	events = pumpUntil(t, backend, "release", func(events []overlayhost.Event) bool {
		return len(events) >= 1
	})
	if events[0].Kind != overlayhost.EventRelease || !events[0].Moved {
		t.Fatalf("release event = %+v, want moved release", events[0])
	}

	backend.Close()
}

func TestBackendRightClickRequestsPetBrowser(t *testing.T) {
	clientEnd, serverEnd := wltest.SocketPair(t)
	defer clientEnd.Close()
	defer serverEnd.Close()
	mock := wltest.NewCompositor(t, serverEnd)
	go mock.Serve()

	backend := NewWithConnect(func() (*wayland.Client, error) {
		return wayland.Handshake(wayland.NewConn(clientEnd))
	})
	scale, err := backend.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	renderer := render.NewRenderer(scale)
	frame, regions := renderer.Compose(render.Input{StateID: "idle"})
	backend.Present(frame)
	backend.SetRegions(regions)
	if err := backend.client.Conn.RoundTrip(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pointer object", func() bool { return mock.BoundID("__pointer") != 0 })

	pointer := mock.BoundID("__pointer")
	surface := mock.BoundID("__surface")
	bodyX := float64((regions.Body.Min.X + regions.Body.Max.X) / 2)
	bodyY := float64((regions.Body.Min.Y + regions.Body.Max.Y) / 2)
	mock.SendEvent(pointer, 0, wayland.Uint(1), wayland.Obj(surface), wayland.Fixed(bodyX), wayland.Fixed(bodyY))
	mock.SendEvent(pointer, 3, wayland.Uint(2), wayland.Uint(0), wayland.Uint(wayland.BtnRight), wayland.Uint(1))

	events := pumpUntil(t, backend, "browser request", func(events []overlayhost.Event) bool {
		for _, event := range events {
			if event.Kind == overlayhost.EventPetBrowserRequest {
				return true
			}
		}
		return false
	})
	if events[len(events)-1].Kind != overlayhost.EventPetBrowserRequest {
		t.Fatalf("last event = %+v, want browser request", events[len(events)-1])
	}
}

func TestBackendApprovalButtonsEmitDecision(t *testing.T) {
	backend := &Backend{
		bufferScale: 1,
		regions: render.Regions{
			Body:            image.Rect(0, 0, 100, 100),
			ApprovalID:      "approval-1",
			ApprovalApprove: image.Rect(20, 20, 60, 42),
			ApprovalDeny:    image.Rect(66, 20, 106, 42),
		},
	}

	backend.pointerX = 30
	backend.pointerY = 30
	backend.handlePress()
	if len(backend.pending) != 1 {
		t.Fatalf("pending events = %+v, want one approve decision", backend.pending)
	}
	if got := backend.pending[0]; got.Kind != overlayhost.EventApprovalDecision ||
		got.ApprovalID != "approval-1" ||
		got.ApprovalDecision != protocol.ApprovalApproved {
		t.Fatalf("approve event = %+v", got)
	}

	backend.pending = nil
	backend.pointerX = 80
	backend.pointerY = 30
	backend.handlePress()
	if len(backend.pending) != 1 {
		t.Fatalf("pending events = %+v, want one deny decision", backend.pending)
	}
	if got := backend.pending[0]; got.Kind != overlayhost.EventApprovalDecision ||
		got.ApprovalID != "approval-1" ||
		got.ApprovalDecision != protocol.ApprovalDenied {
		t.Fatalf("deny event = %+v", got)
	}
}
