package usbip

// wire.go is the protocol between the server and the browser's WebUSB
// code (web/assets/usb.js) over a reliable, ordered data channel labelled
// "usb". One channel carries one device.
//
// The channel is treated as a byte stream so that transfers larger than a
// data channel message (Chrome: 256 KiB; pion: 64 KiB by default) work:
// every logical message is a little-endian u32 length followed by that
// many bytes, and the stream is cut into chunks of at most ChunkSize.
//
// The server does all USB/IP-specific translation; the browser is a thin
// executor that maps each operation onto one WebUSB call.
//
// Browser → server:
//
//	Hello    01 ver:u8 activeConfig:u8 nconfigs:u8 devLen:u16 dev[devLen]
//	         { cfgLen:u16 cfg[cfgLen] } × nconfigs  nameLen:u16 name[nameLen]
//	Done     02 id:u32 status:i32 actual:u32 npackets:u32
//	         { status:i32 actual:u32 } × npackets  data[...]
//
// Server → browser:
//
//	Status   80 code:u8 text[...]                (code: StatusReady/…)
//	Control  10 id:u32 setup[8] data[...]        (IN length is wLength)
//	Transfer 11 id:u32 ep:u8 flags:u8 length:u32 data[...]
//	Iso      12 id:u32 ep:u8 npackets:u32 { length:u32 } × npackets data[...]
//	SetConf  13 id:u32 value:u8
//	SetAlt   14 id:u32 iface:u8 alt:u8
//	Clear    15 id:u32 ep:u8
//	Reset    16 id:u32
//
// "status" is a negative Linux errno (0 = success, -32 = stall, ...).
// For IN operations "data" is what was received (for iso: each packet's
// actual bytes, concatenated); for OUT operations it is empty and
// "actual" is the number of bytes written.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const WireVersion = 1

// Message types.
const (
	MsgHello    = 0x01
	MsgDone     = 0x02
	MsgStatus   = 0x80
	MsgControl  = 0x10
	MsgTransfer = 0x11
	MsgIso      = 0x12
	MsgSetConf  = 0x13
	MsgSetAlt   = 0x14
	MsgClear    = 0x15
	MsgReset    = 0x16
)

// Status codes sent in MsgStatus.
const (
	StatusReady    = 0 // channel accepted; the browser may send Hello
	StatusAttached = 1 // device attached to the host
	StatusError    = 2 // fatal; the channel will be closed
)

// TransferZLP asks the browser to follow an OUT transfer with a
// zero-length packet (URB_ZERO_PACKET).
const TransferZLP = 0x01

// ChunkSize is the largest data channel message we send.
const ChunkSize = 16 << 10

// maxFrame bounds a logical message read from the browser.
const maxFrame = maxTransfer + 64*1024

// Framer reassembles length-prefixed messages from data channel chunks.
type Framer struct{ buf []byte }

// Feed appends a chunk and calls fn for each complete message.
func (f *Framer) Feed(chunk []byte, fn func([]byte)) error {
	f.buf = append(f.buf, chunk...)
	for len(f.buf) >= 4 {
		n := binary.LittleEndian.Uint32(f.buf)
		if n > maxFrame {
			return fmt.Errorf("usbip: frame of %d bytes is too large", n)
		}
		if uint32(len(f.buf)-4) < n {
			break
		}
		msg := f.buf[4 : 4+n]
		fn(bytes.Clone(msg)) // callers may keep it
		f.buf = f.buf[4+n:]
	}
	if len(f.buf) == 0 {
		f.buf = nil // release a large buffer once drained
	}
	return nil
}

// Frame splits a length-prefixed msg into chunks of at most ChunkSize.
func Frame(msg []byte) [][]byte {
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(msg)), uint32(len(msg)))
	b = append(b, msg...)
	var out [][]byte
	for len(b) > ChunkSize {
		out = append(out, b[:ChunkSize])
		b = b[ChunkSize:]
	}
	return append(out, b)
}

// Hello describes the browser's device. Descriptors are raw bytes as
// read with GET_DESCRIPTOR.
type Hello struct {
	ActiveConfig uint8
	Device       []byte
	Configs      [][]byte
	Name         string
}

func (h *Hello) Marshal() []byte {
	b := []byte{MsgHello, WireVersion, h.ActiveConfig, byte(len(h.Configs))}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(h.Device)))
	b = append(b, h.Device...)
	for _, c := range h.Configs {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(c)))
		b = append(b, c...)
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(h.Name)))
	return append(b, h.Name...)
}

var errShort = errors.New("usbip: short message")

type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errShort
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
func (r *reader) u8() uint8 {
	if v := r.take(1); v != nil {
		return v[0]
	}
	return 0
}
func (r *reader) u16() uint16 {
	if v := r.take(2); v != nil {
		return binary.LittleEndian.Uint16(v)
	}
	return 0
}
func (r *reader) u32() uint32 {
	if v := r.take(4); v != nil {
		return binary.LittleEndian.Uint32(v)
	}
	return 0
}

func UnmarshalHello(b []byte) (*Hello, error) {
	r := reader{b: b}
	if r.u8() != MsgHello {
		return nil, errors.New("usbip: not a hello")
	}
	if v := r.u8(); r.err == nil && v != WireVersion {
		return nil, fmt.Errorf("usbip: browser speaks wire version %d, want %d", v, WireVersion)
	}
	h := &Hello{ActiveConfig: r.u8()}
	n := int(r.u8())
	h.Device = r.take(int(r.u16()))
	for range n {
		h.Configs = append(h.Configs, r.take(int(r.u16())))
	}
	h.Name = string(r.take(int(r.u16())))
	if r.err != nil {
		return nil, r.err
	}
	return h, nil
}

// PacketResult is one isochronous packet's outcome.
type PacketResult struct {
	Status int32
	Actual uint32
}

// Done is the browser's completion of operation ID.
type Done struct {
	ID      uint32
	Status  int32
	Actual  uint32
	Packets []PacketResult
	Data    []byte
}

func (d *Done) Marshal() []byte {
	b := []byte{MsgDone}
	b = binary.LittleEndian.AppendUint32(b, d.ID)
	b = binary.LittleEndian.AppendUint32(b, uint32(d.Status))
	b = binary.LittleEndian.AppendUint32(b, d.Actual)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(d.Packets)))
	for _, p := range d.Packets {
		b = binary.LittleEndian.AppendUint32(b, uint32(p.Status))
		b = binary.LittleEndian.AppendUint32(b, p.Actual)
	}
	return append(b, d.Data...)
}

func UnmarshalDone(b []byte) (*Done, error) {
	r := reader{b: b}
	if r.u8() != MsgDone {
		return nil, errors.New("usbip: not a done")
	}
	d := &Done{ID: r.u32(), Status: int32(r.u32()), Actual: r.u32()}
	n := r.u32()
	if r.err == nil && n > maxIsoPackets {
		return nil, errTooLarge
	}
	for range n {
		d.Packets = append(d.Packets, PacketResult{int32(r.u32()), r.u32()})
	}
	if r.err != nil {
		return nil, r.err
	}
	d.Data = r.b
	return d, nil
}

// Op is a server → browser operation.
type Op struct {
	Type   uint8
	ID     uint32
	Setup  [8]byte  // Control
	EP     uint8    // Transfer, Iso, Clear: endpoint address incl. 0x80 for IN
	Flags  uint8    // Transfer
	Length uint32   // Transfer IN
	Iso    []uint32 // Iso: packet lengths
	Value  uint8    // SetConf: configuration value; SetAlt: interface
	Alt    uint8    // SetAlt
	Data   []byte   // OUT payload
}

func (o *Op) Marshal() []byte {
	b := []byte{o.Type}
	b = binary.LittleEndian.AppendUint32(b, o.ID)
	switch o.Type {
	case MsgControl:
		b = append(b, o.Setup[:]...)
	case MsgTransfer:
		b = append(b, o.EP, o.Flags)
		b = binary.LittleEndian.AppendUint32(b, o.Length)
	case MsgIso:
		b = append(b, o.EP)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(o.Iso)))
		for _, l := range o.Iso {
			b = binary.LittleEndian.AppendUint32(b, l)
		}
	case MsgSetConf:
		b = append(b, o.Value)
	case MsgSetAlt:
		b = append(b, o.Value, o.Alt)
	case MsgClear:
		b = append(b, o.EP)
	}
	return append(b, o.Data...)
}

// UnmarshalOp decodes an operation (the browser's side; used by tests).
func UnmarshalOp(b []byte) (*Op, error) {
	r := reader{b: b}
	o := &Op{Type: r.u8(), ID: r.u32()}
	switch o.Type {
	case MsgControl:
		copy(o.Setup[:], r.take(8))
	case MsgTransfer:
		o.EP, o.Flags, o.Length = r.u8(), r.u8(), r.u32()
	case MsgIso:
		o.EP = r.u8()
		n := r.u32()
		if r.err == nil && n > maxIsoPackets {
			return nil, errTooLarge
		}
		for range n {
			o.Iso = append(o.Iso, r.u32())
		}
	case MsgSetConf:
		o.Value = r.u8()
	case MsgSetAlt:
		o.Value, o.Alt = r.u8(), r.u8()
	case MsgClear:
		o.EP = r.u8()
	case MsgReset:
	default:
		return nil, fmt.Errorf("usbip: unknown op %#x", o.Type)
	}
	if r.err != nil {
		return nil, r.err
	}
	o.Data = r.b
	return o, nil
}

// StatusMsg encodes a MsgStatus.
func StatusMsg(code uint8, text string) []byte {
	return append([]byte{MsgStatus, code}, text...)
}
