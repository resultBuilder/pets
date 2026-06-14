package wayland

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Opcodes and enum values for the protocol subset the overlay uses; taken
// from wayland.xml and wlr-layer-shell-unstable-v1.xml.
const (
	opDisplaySync        = 0
	opDisplayGetRegistry = 1

	opRegistryBind = 0

	opCompositorCreateSurface = 0
	opCompositorCreateRegion  = 1

	opSurfaceDestroy        = 0
	opSurfaceAttach         = 1
	opSurfaceDamage         = 2
	opSurfaceSetInputRegion = 5
	opSurfaceCommit         = 6
	opSurfaceSetBufferScale = 8

	opRegionDestroy = 0
	opRegionAdd     = 1

	opShmCreatePool      = 0
	opShmPoolCreateBuf   = 0
	opShmPoolDestroy     = 1
	opBufferDestroy      = 0
	shmFormatARGB8888    = 0
	opSeatGetPointer     = 0
	seatCapabilityCursor = 1

	opLayerShellGetLayerSurface = 0

	opLayerSurfaceSetSize           = 0
	opLayerSurfaceSetAnchor         = 1
	opLayerSurfaceSetMargin         = 3
	opLayerSurfaceSetKeyboardInter  = 4
	opLayerSurfaceAckConfigure      = 6

	LayerTop     = 2
	LayerOverlay = 3

	AnchorTop    = 1
	AnchorBottom = 2
	AnchorLeft   = 4
	AnchorRight  = 8

	// Linux input button code for the left mouse button.
	BtnLeft = 272
)

// Global is one wl_registry announcement.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Client builds on Conn with the typed helpers the overlay backend needs.
type Client struct {
	Conn     *Conn
	Globals  []Global
	registry uint32
}

// Connect dials the compositor and collects the registry globals.
func Connect() (*Client, error) {
	conn, err := Dial()
	if err != nil {
		return nil, err
	}
	return Handshake(conn)
}

// Handshake performs the registry exchange on an established connection
// (tests drive it over a socketpair against a mock compositor).
func Handshake(conn *Conn) (*Client, error) {
	client := &Client{Conn: conn}
	client.registry = conn.NewID(func(opcode uint16, r *Reader) {
		if opcode == 0 { // global
			client.Globals = append(client.Globals, Global{
				Name:      r.Uint(),
				Interface: r.String(),
				Version:   r.Uint(),
			})
		}
	})
	if err := conn.Request(displayObjectID, opDisplayGetRegistry, NewID(client.registry)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.RoundTrip(); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

// Bind attaches a registry global, returning the new object id.
func (c *Client) Bind(interfaceName string, maxVersion uint32, handler EventHandler) (uint32, error) {
	for _, global := range c.Globals {
		if global.Interface != interfaceName {
			continue
		}
		version := global.Version
		if version > maxVersion {
			version = maxVersion
		}
		id := c.Conn.NewID(handler)
		err := c.Conn.Request(c.registry, opRegistryBind,
			Uint(global.Name), Str(interfaceName), Uint(version), NewID(id))
		return id, err
	}
	return 0, fmt.Errorf("wayland: compositor does not provide %s", interfaceName)
}

// ShmPool is a shared-memory pool mapped on both sides.
type ShmPool struct {
	conn *Conn
	id   uint32
	fd   int
	Data []byte
}

// CreateShmPool makes a pool of size bytes backed by an anonymous file.
func (c *Client) CreateShmPool(shm uint32, size int) (*ShmPool, error) {
	fd, err := newShmFile(size)
	if err != nil {
		return nil, err
	}
	data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	id := c.Conn.NewID(nil)
	if err := c.Conn.Request(shm, opShmCreatePool, NewID(id), FD(fd), Int(size)); err != nil {
		_ = unix.Munmap(data)
		unix.Close(fd)
		return nil, err
	}
	return &ShmPool{conn: c.Conn, id: id, fd: fd, Data: data}, nil
}

// CreateBuffer carves an ARGB8888 buffer out of the pool. The release
// handler tracks when the compositor stops reading it.
func (p *ShmPool) CreateBuffer(offset int, width int, height int, stride int, onRelease func()) (uint32, error) {
	id := p.conn.NewID(func(opcode uint16, r *Reader) {
		if opcode == 0 && onRelease != nil {
			onRelease()
		}
	})
	err := p.conn.Request(p.id, opShmPoolCreateBuf,
		NewID(id), Int(offset), Int(width), Int(height), Int(stride), Uint(shmFormatARGB8888))
	return id, err
}

func (p *ShmPool) Destroy() {
	_ = p.conn.Request(p.id, opShmPoolDestroy)
	_ = unix.Munmap(p.Data)
	unix.Close(p.fd)
	p.Data = nil
}
