// Package wltest is a mock Wayland compositor for tests: it answers the
// handshake, tracks binds, and acks layer-shell configuration so backend
// logic can be exercised on any development host.
package wltest

import (
	"encoding/binary"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"codex-pets/internal/wayland"
)

// SocketPair builds two connected *net.UnixConn ends with SCM_RIGHTS support.
func SocketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(fd int) *net.UnixConn {
		file := os.NewFile(uintptr(fd), "socketpair")
		conn, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		return conn.(*net.UnixConn)
	}
	return wrap(fds[0]), wrap(fds[1])
}

// Compositor mocks the protocol subset the overlay uses.
type Compositor struct {
	T    *testing.T
	Conn *net.UnixConn
	// Globals advertised on get_registry.
	Globals []wayland.Global

	mu         sync.Mutex
	fds        []int
	bound      map[string]uint32 // interface -> object id
	commits    int
	margins    [4]int32
	inputRects [][4]int32
}

func NewCompositor(t *testing.T, conn *net.UnixConn) *Compositor {
	return &Compositor{
		T:    t,
		Conn: conn,
		Globals: []wayland.Global{
			{Name: 1, Interface: "wl_compositor", Version: 4},
			{Name: 2, Interface: "wl_shm", Version: 1},
			{Name: 3, Interface: "zwlr_layer_shell_v1", Version: 1},
			{Name: 4, Interface: "wl_seat", Version: 5},
			{Name: 5, Interface: "wl_output", Version: 2},
		},
		bound: map[string]uint32{},
	}
}

func (m *Compositor) FDs() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int{}, m.fds...)
}

func (m *Compositor) Commits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commits
}

func (m *Compositor) Margins() [4]int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.margins
}

func (m *Compositor) InputRects() [][4]int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][4]int32{}, m.inputRects...)
}

func (m *Compositor) BoundID(interfaceName string) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bound[interfaceName]
}

// SendEvent emits a compositor event to the client.
func (m *Compositor) SendEvent(object uint32, opcode uint16, args ...any) {
	data, _, err := wayland.EncodeForTest(object, opcode, args...)
	if err != nil {
		m.T.Errorf("mock encode: %v", err)
		return
	}
	if _, err := m.Conn.Write(data); err != nil {
		m.T.Errorf("mock write: %v", err)
	}
}

// Serve processes client requests until the socket closes.
func (m *Compositor) Serve() {
	buf := []byte{}
	chunk := make([]byte, 8192)
	oob := make([]byte, 512)
	for {
		_ = m.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, oobn, _, _, err := m.Conn.ReadMsgUnix(chunk, oob)
		if err != nil {
			return
		}
		if oobn > 0 {
			if messages, err := unix.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, message := range messages {
					if fds, err := unix.ParseUnixRights(&message); err == nil {
						m.mu.Lock()
						m.fds = append(m.fds, fds...)
						m.mu.Unlock()
					}
				}
			}
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			object := binary.LittleEndian.Uint32(buf)
			word := binary.LittleEndian.Uint32(buf[4:])
			size := int(word >> 16)
			opcode := uint16(word & 0xffff)
			if len(buf) < size {
				break
			}
			m.handle(object, opcode, wayland.NewReaderForTest(buf[8:size]))
			buf = buf[size:]
		}
	}
}

func (m *Compositor) handle(object uint32, opcode uint16, r *wayland.Reader) {
	layerShell := m.BoundID("zwlr_layer_shell_v1")
	compositor := m.BoundID("wl_compositor")
	surface := m.BoundID("__surface")
	layer := m.BoundID("__layer")
	switch {
	case object == 1 && opcode == 0: // sync
		callback := r.Uint()
		m.SendEvent(callback, 0, wayland.Uint(1))
		m.SendEvent(1, 1, wayland.Uint(callback))
	case object == 1 && opcode == 1: // get_registry
		registry := r.Uint()
		for _, global := range m.Globals {
			m.SendEvent(registry, 0, wayland.Uint(global.Name), wayland.Str(global.Interface), wayland.Uint(global.Version))
		}
	case opcode == 0 && objectIsRegistry(object, m): // bind
		name := r.Uint()
		interfaceName := r.String()
		_ = r.Uint() // version
		id := r.Uint()
		m.mu.Lock()
		m.bound[interfaceName] = id
		m.mu.Unlock()
		_ = name
		switch interfaceName {
		case "wl_seat":
			m.SendEvent(id, 0, wayland.Uint(1)) // capabilities: pointer
		case "wl_output":
			m.SendEvent(id, 3, wayland.Int(1)) // scale 1
		}
	case compositor != 0 && object == compositor && opcode == 0: // create_surface
		m.mu.Lock()
		m.bound["__surface"] = r.Uint()
		m.mu.Unlock()
	case compositor != 0 && object == compositor && opcode == 1: // create_region
		m.mu.Lock()
		m.bound["__region"] = r.Uint()
		m.mu.Unlock()
	case object != 0 && object == m.BoundID("__region") && opcode == 1: // region.add
		rect := [4]int32{r.Int(), r.Int(), r.Int(), r.Int()}
		m.mu.Lock()
		m.inputRects = append(m.inputRects, rect)
		m.mu.Unlock()
	case object != 0 && object == m.BoundID("wl_seat") && opcode == 0: // get_pointer
		m.mu.Lock()
		m.bound["__pointer"] = r.Uint()
		m.mu.Unlock()
	case layerShell != 0 && object == layerShell && opcode == 0: // get_layer_surface
		id := r.Uint()
		m.mu.Lock()
		m.bound["__layer"] = id
		m.mu.Unlock()
	case layer != 0 && object == layer && opcode == 3: // set_margin
		m.mu.Lock()
		m.margins = [4]int32{r.Int(), r.Int(), r.Int(), r.Int()}
		m.mu.Unlock()
	case surface != 0 && object == surface && opcode == 6: // commit
		m.mu.Lock()
		m.commits++
		first := m.commits == 1
		m.mu.Unlock()
		if first {
			// Configure the layer surface after the initial commit.
			m.SendEvent(layer, 0, wayland.Uint(7), wayland.Uint(0), wayland.Uint(0))
		}
	}
}

func objectIsRegistry(object uint32, m *Compositor) bool {
	// The registry is the first client-allocated id after wl_display.
	return object == 2
}
