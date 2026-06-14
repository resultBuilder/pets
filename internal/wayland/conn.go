package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const displayObjectID = 1

// EventHandler receives an object's events; the reader is positioned at the
// first argument.
type EventHandler func(opcode uint16, r *Reader)

// Conn is a Wayland client connection. Event dispatch happens on the
// goroutine that calls Pump/RoundTrip; requests may come from that same
// goroutine (the overlay host loop).
type Conn struct {
	socket *net.UnixConn

	writeMu sync.Mutex

	nextID   uint32
	handlers map[uint32]EventHandler

	readBuf []byte
	fdQueue []int

	fatal error
}

// Dial connects to $XDG_RUNTIME_DIR/$WAYLAND_DISPLAY.
func Dial() (*Conn, error) {
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		return nil, errors.New("WAYLAND_DISPLAY is not set")
	}
	path := display
	if !filepath.IsAbs(path) {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			return nil, errors.New("XDG_RUNTIME_DIR is not set")
		}
		path = filepath.Join(runtimeDir, display)
	}
	socket, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return NewConn(socket), nil
}

// NewConn wraps an established socket (tests inject a socketpair end).
func NewConn(socket *net.UnixConn) *Conn {
	conn := &Conn{
		socket:   socket,
		nextID:   displayObjectID,
		handlers: map[uint32]EventHandler{},
	}
	conn.handlers[displayObjectID] = conn.displayEvent
	return conn
}

func (c *Conn) Close() {
	_ = c.socket.Close()
}

// Err reports a fatal protocol error (wl_display.error or a read failure).
func (c *Conn) Err() error {
	return c.fatal
}

// NewID allocates an object id and registers its event handler.
func (c *Conn) NewID(handler EventHandler) uint32 {
	c.nextID++
	id := c.nextID
	if handler != nil {
		c.handlers[id] = handler
	}
	return id
}

// SetHandler replaces an object's event handler.
func (c *Conn) SetHandler(id uint32, handler EventHandler) {
	c.handlers[id] = handler
}

// Request sends one message; FD arguments travel as ancillary data.
func (c *Conn) Request(object uint32, opcode uint16, args ...any) error {
	if c.fatal != nil {
		return c.fatal
	}
	data, fds, err := encodeRequest(object, opcode, args...)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if len(fds) > 0 {
		rights := unix.UnixRights(fds...)
		_, _, err = c.socket.WriteMsgUnix(data, rights, nil)
		return err
	}
	_, err = c.socket.Write(data)
	return err
}

// displayEvent handles wl_display.error and delete_id.
func (c *Conn) displayEvent(opcode uint16, r *Reader) {
	switch opcode {
	case 0: // error
		object := r.Uint()
		code := r.Uint()
		message := r.String()
		c.fatal = fmt.Errorf("wayland protocol error on object %d (code %d): %s", object, code, message)
	case 1: // delete_id
		delete(c.handlers, r.Uint())
	}
}

// Pump reads and dispatches everything available without blocking longer
// than the deadline. A zero deadline polls.
func (c *Conn) Pump(deadline time.Time) error {
	if c.fatal != nil {
		return c.fatal
	}
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Millisecond)
	}
	for {
		if err := c.readChunk(deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			c.fatal = err
			return err
		}
		// After the first successful read, drain whatever else is queued.
		deadline = time.Now().Add(time.Millisecond)
	}
	c.dispatchBuffered()
	return c.fatal
}

// RoundTrip issues wl_display.sync and pumps until the callback fires,
// guaranteeing all previous requests were processed.
func (c *Conn) RoundTrip() error {
	done := false
	callback := c.NewID(func(opcode uint16, r *Reader) {
		done = true
	})
	if err := c.Request(displayObjectID, 0, NewID(callback)); err != nil {
		return err
	}
	timeout := time.Now().Add(5 * time.Second)
	for !done {
		if time.Now().After(timeout) {
			return errors.New("wayland round trip timed out")
		}
		if err := c.readChunk(time.Now().Add(time.Second)); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			c.fatal = err
			return err
		}
		c.dispatchBuffered()
		if c.fatal != nil {
			return c.fatal
		}
	}
	return nil
}

func (c *Conn) readChunk(deadline time.Time) error {
	_ = c.socket.SetReadDeadline(deadline)
	buf := make([]byte, 64*1024)
	oob := make([]byte, 1024)
	n, oobn, _, _, err := c.socket.ReadMsgUnix(buf, oob)
	if err != nil {
		return err
	}
	if oobn > 0 {
		messages, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err == nil {
			for _, message := range messages {
				if fds, err := unix.ParseUnixRights(&message); err == nil {
					c.fdQueue = append(c.fdQueue, fds...)
				}
			}
		}
	}
	c.readBuf = append(c.readBuf, buf[:n]...)
	return nil
}

func (c *Conn) dispatchBuffered() {
	for len(c.readBuf) >= 8 {
		object := binary.LittleEndian.Uint32(c.readBuf)
		word := binary.LittleEndian.Uint32(c.readBuf[4:])
		size := int(word >> 16)
		opcode := uint16(word & 0xffff)
		if size < 8 || len(c.readBuf) < size {
			return
		}
		body := c.readBuf[8:size]
		if handler, ok := c.handlers[object]; ok && handler != nil {
			handler(opcode, &Reader{data: body})
		}
		c.readBuf = c.readBuf[size:]
	}
}

// TakeFD pops the oldest received file descriptor (events with fd args).
func (c *Conn) TakeFD() (int, bool) {
	if len(c.fdQueue) == 0 {
		return -1, false
	}
	fd := c.fdQueue[0]
	c.fdQueue = c.fdQueue[1:]
	return fd, true
}
