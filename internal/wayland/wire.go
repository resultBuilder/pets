// Package wayland is a minimal, dependency-free Wayland client: the wire
// protocol is length-prefixed words over a Unix socket with file descriptors
// in ancillary data, so the small subset the pet overlay needs (registry,
// compositor, shm, seat/pointer, outputs, wlr-layer-shell) does not justify
// linking libwayland through cgo.
package wayland

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Argument marker types for Conn.Request.
type (
	Uint  uint32
	Int   int32
	Fixed float64
	Str   string
	Obj   uint32 // existing object id (0 = null)
	NewID uint32 // id allocated by NewID
	FD    int    // file descriptor passed via SCM_RIGHTS
	Arr   []byte
)

func appendWord(buf []byte, word uint32) []byte {
	var scratch [4]byte
	binary.LittleEndian.PutUint32(scratch[:], word)
	return append(buf, scratch[:]...)
}

func padded(length int) int {
	return (length + 3) &^ 3
}

// encodeRequest builds a wire message; fds are returned separately for the
// ancillary payload.
func encodeRequest(object uint32, opcode uint16, args ...any) ([]byte, []int, error) {
	body := []byte{}
	fds := []int{}
	for _, arg := range args {
		switch value := arg.(type) {
		case Uint:
			body = appendWord(body, uint32(value))
		case Int:
			body = appendWord(body, uint32(int32(value)))
		case Obj:
			body = appendWord(body, uint32(value))
		case NewID:
			body = appendWord(body, uint32(value))
		case Fixed:
			body = appendWord(body, uint32(int32(math.Round(float64(value)*256))))
		case Str:
			data := []byte(value)
			body = appendWord(body, uint32(len(data)+1))
			body = append(body, data...)
			body = append(body, 0)
			for len(body)%4 != 0 {
				body = append(body, 0)
			}
		case Arr:
			body = appendWord(body, uint32(len(value)))
			body = append(body, value...)
			for len(body)%4 != 0 {
				body = append(body, 0)
			}
		case FD:
			fds = append(fds, int(value))
		default:
			return nil, nil, fmt.Errorf("wayland: unsupported argument %T", arg)
		}
	}
	size := 8 + len(body)
	if size > 0xffff {
		return nil, nil, fmt.Errorf("wayland: message too large (%d bytes)", size)
	}
	header := appendWord(nil, object)
	header = appendWord(header, uint32(size)<<16|uint32(opcode))
	return append(header, body...), fds, nil
}

// Reader decodes event arguments in order.
type Reader struct {
	data []byte
	off  int
}

func (r *Reader) word() uint32 {
	if r.off+4 > len(r.data) {
		return 0
	}
	value := binary.LittleEndian.Uint32(r.data[r.off:])
	r.off += 4
	return value
}

func (r *Reader) Uint() uint32 { return r.word() }
func (r *Reader) Int() int32   { return int32(r.word()) }

func (r *Reader) Fixed() float64 {
	return float64(int32(r.word())) / 256
}

func (r *Reader) String() string {
	length := int(r.word())
	if length == 0 || r.off+length > len(r.data) {
		return ""
	}
	value := string(r.data[r.off : r.off+length-1])
	r.off += padded(length)
	return value
}

// EncodeForTest and NewReaderForTest expose the codec to test helpers.
func EncodeForTest(object uint32, opcode uint16, args ...any) ([]byte, []int, error) {
	return encodeRequest(object, opcode, args...)
}

func NewReaderForTest(data []byte) *Reader {
	return &Reader{data: data}
}
