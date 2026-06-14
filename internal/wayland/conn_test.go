package wayland

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// socketPair builds two connected *net.UnixConn ends with SCM_RIGHTS support.
func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
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

// mockCompositor answers the subset of requests the handshake needs.
type mockCompositor struct {
	t    *testing.T
	conn *net.UnixConn
	fds  []int
}

func (m *mockCompositor) sendEvent(object uint32, opcode uint16, args ...any) {
	data, _, err := encodeRequest(object, opcode, args...)
	if err != nil {
		m.t.Errorf("mock encode: %v", err)
		return
	}
	if _, err := m.conn.Write(data); err != nil {
		m.t.Errorf("mock write: %v", err)
	}
}

func (m *mockCompositor) serve() {
	buf := []byte{}
	chunk := make([]byte, 4096)
	oob := make([]byte, 512)
	for {
		_ = m.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, oobn, _, _, err := m.conn.ReadMsgUnix(chunk, oob)
		if err != nil {
			return
		}
		if oobn > 0 {
			if messages, err := unix.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, message := range messages {
					if fds, err := unix.ParseUnixRights(&message); err == nil {
						m.fds = append(m.fds, fds...)
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
			body := &Reader{data: buf[8:size]}
			m.handle(object, opcode, body)
			buf = buf[size:]
		}
	}
}

func (m *mockCompositor) handle(object uint32, opcode uint16, r *Reader) {
	switch {
	case object == 1 && opcode == opDisplaySync:
		callback := r.Uint()
		m.sendEvent(callback, 0, Uint(1)) // wl_callback.done
		m.sendEvent(1, 1, Uint(callback)) // wl_display.delete_id
	case object == 1 && opcode == opDisplayGetRegistry:
		registry := r.Uint()
		m.sendEvent(registry, 0, Uint(1), Str("wl_compositor"), Uint(4))
		m.sendEvent(registry, 0, Uint(2), Str("wl_shm"), Uint(1))
		m.sendEvent(registry, 0, Uint(3), Str("zwlr_layer_shell_v1"), Uint(1))
	}
}

func TestHandshakeCollectsGlobalsAndBinds(t *testing.T) {
	clientEnd, serverEnd := socketPair(t)
	defer clientEnd.Close()
	defer serverEnd.Close()
	mock := &mockCompositor{t: t, conn: serverEnd}
	go mock.serve()

	client, err := Handshake(NewConn(clientEnd))
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Globals) != 3 {
		t.Fatalf("globals = %+v, want 3", client.Globals)
	}
	if client.Globals[2].Interface != "zwlr_layer_shell_v1" {
		t.Fatalf("layer shell global missing: %+v", client.Globals)
	}

	if _, err := client.Bind("wl_compositor", 4, nil); err != nil {
		t.Fatalf("bind compositor: %v", err)
	}
	if _, err := client.Bind("wp_nonexistent", 1, nil); err == nil {
		t.Fatal("binding a missing global must fail (drives the X11 fallback)")
	}
}

func TestShmPoolPassesFileDescriptor(t *testing.T) {
	clientEnd, serverEnd := socketPair(t)
	defer clientEnd.Close()
	defer serverEnd.Close()
	mock := &mockCompositor{t: t, conn: serverEnd}
	go mock.serve()

	client, err := Handshake(NewConn(clientEnd))
	if err != nil {
		t.Fatal(err)
	}
	shm, err := client.Bind("wl_shm", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := client.CreateShmPool(shm, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Destroy()
	pool.Data[0] = 0xAB
	if err := client.Conn.RoundTrip(); err != nil {
		t.Fatal(err)
	}
	if len(mock.fds) != 1 {
		t.Fatalf("compositor received %d fds, want 1", len(mock.fds))
	}
	// The compositor maps the same memory the client writes.
	mapped, err := unix.Mmap(mock.fds[0], 0, 4096, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(mapped)
	if mapped[0] != 0xAB {
		t.Fatalf("shared memory not visible through passed fd: %x", mapped[0])
	}
}

func TestWireEncodingRoundTrip(t *testing.T) {
	data, fds, err := encodeRequest(7, 3, Uint(42), Int(-5), Str("пет"), Fixed(1.5), Obj(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 0 {
		t.Fatalf("unexpected fds: %v", fds)
	}
	if object := binary.LittleEndian.Uint32(data); object != 7 {
		t.Fatalf("object = %d", object)
	}
	word := binary.LittleEndian.Uint32(data[4:])
	if int(word>>16) != len(data) || uint16(word&0xffff) != 3 {
		t.Fatalf("header word = %x (len %d)", word, len(data))
	}
	r := &Reader{data: data[8:]}
	if r.Uint() != 42 || r.Int() != -5 {
		t.Fatal("integer args did not round-trip")
	}
	if text := r.String(); text != "пет" {
		t.Fatalf("string arg = %q", text)
	}
	if fixed := r.Fixed(); fixed != 1.5 {
		t.Fatalf("fixed arg = %v", fixed)
	}
	if r.Uint() != 9 {
		t.Fatal("object arg did not round-trip")
	}
}
