// Package usbip exposes a USB device that lives elsewhere (here: in the
// viewer's browser, via WebUSB) to the local kernel as a real USB device,
// using the USB/IP protocol.
//
// protocol.go is the USB/IP wire format, as described in the Linux
// kernel's Documentation/usb/usbip_protocol.rst. All fields are big-endian.
// It is pure encode/decode so it can be tested against golden bytes.
package usbip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Version is the USB/IP protocol version sent in OP_* headers.
const Version = 0x0111

// OP_* codes for the import handshake (before the URB phase).
const (
	OpReqDevlist = 0x8005
	OpRepDevlist = 0x0005
	OpReqImport  = 0x8003
	OpRepImport  = 0x0003
)

// USBIP_* commands in the URB phase.
const (
	CmdSubmit = 0x00000001
	CmdUnlink = 0x00000002
	RetSubmit = 0x00000003
	RetUnlink = 0x00000004
)

// Directions in the URB header.
const (
	DirOut = 0
	DirIn  = 1
)

// USB speeds as the kernel numbers them (enum usb_device_speed).
const (
	SpeedUnknown   = 0
	SpeedLow       = 1
	SpeedFull      = 2
	SpeedHigh      = 3
	SpeedWireless  = 4
	SpeedSuper     = 5
	SpeedSuperPlus = 6
)

// Linux URB transfer_flags that matter to us.
const (
	URBShortNotOK = 0x0001
	URBZeroPacket = 0x0040
)

// Negative Linux errno values used as URB status.
const (
	StatusOK         = 0
	StatusENOENT     = -2
	StatusEIO        = -5
	StatusENODEV     = -19
	StatusEPIPE      = -32 // stall
	StatusEPROTO     = -71
	StatusEOVERFLOW  = -75 // babble
	StatusECONNRESET = -104
	StatusESHUTDOWN  = -108
	StatusEREMOTEIO  = -121 // short packet with URB_SHORT_NOT_OK
)

// NoPackets is number_of_packets for non-isochronous URBs.
const NoPackets = 0xffffffff

// OpHeader starts every handshake message.
type OpHeader struct {
	Version uint16
	Code    uint16
	Status  uint32
}

const opHeaderLen = 8

func (h OpHeader) Marshal() []byte {
	b := make([]byte, opHeaderLen)
	binary.BigEndian.PutUint16(b[0:], h.Version)
	binary.BigEndian.PutUint16(b[2:], h.Code)
	binary.BigEndian.PutUint32(b[4:], h.Status)
	return b
}

func ReadOpHeader(r io.Reader) (OpHeader, error) {
	var b [opHeaderLen]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return OpHeader{}, err
	}
	return OpHeader{
		Version: binary.BigEndian.Uint16(b[0:]),
		Code:    binary.BigEndian.Uint16(b[2:]),
		Status:  binary.BigEndian.Uint32(b[4:]),
	}, nil
}

// Interface is the per-interface record in OP_REP_DEVLIST.
type Interface struct {
	Class, SubClass, Protocol uint8
}

// DeviceInfo is the exported-device record shared by OP_REP_DEVLIST and
// OP_REP_IMPORT (312 bytes; the devlist appends one 4-byte record per
// interface).
type DeviceInfo struct {
	Path                string // up to 255 bytes
	BusID               string // up to 31 bytes, e.g. "1-1"
	BusNum, DevNum      uint32
	Speed               uint32
	IDVendor, IDProduct uint16
	BCDDevice           uint16
	DeviceClass         uint8
	DeviceSubClass      uint8
	DeviceProtocol      uint8
	ConfigurationValue  uint8
	NumConfigurations   uint8
	NumInterfaces       uint8
	Interfaces          []Interface // devlist only
}

const deviceInfoLen = 312

func (d *DeviceInfo) marshal(withInterfaces bool) []byte {
	n := deviceInfoLen
	if withInterfaces {
		n += 4 * len(d.Interfaces)
	}
	b := make([]byte, n)
	copy(b[0:255], d.Path)
	copy(b[256:287], d.BusID)
	o := 288
	binary.BigEndian.PutUint32(b[o:], d.BusNum)
	binary.BigEndian.PutUint32(b[o+4:], d.DevNum)
	binary.BigEndian.PutUint32(b[o+8:], d.Speed)
	binary.BigEndian.PutUint16(b[o+12:], d.IDVendor)
	binary.BigEndian.PutUint16(b[o+14:], d.IDProduct)
	binary.BigEndian.PutUint16(b[o+16:], d.BCDDevice)
	b[o+18] = d.DeviceClass
	b[o+19] = d.DeviceSubClass
	b[o+20] = d.DeviceProtocol
	b[o+21] = d.ConfigurationValue
	b[o+22] = d.NumConfigurations
	b[o+23] = d.NumInterfaces
	if withInterfaces {
		for i, it := range d.Interfaces {
			p := deviceInfoLen + 4*i
			b[p], b[p+1], b[p+2] = it.Class, it.SubClass, it.Protocol
		}
	}
	return b
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func unmarshalDeviceInfo(b []byte) DeviceInfo {
	o := 288
	return DeviceInfo{
		Path:               cstr(b[0:256]),
		BusID:              cstr(b[256:288]),
		BusNum:             binary.BigEndian.Uint32(b[o:]),
		DevNum:             binary.BigEndian.Uint32(b[o+4:]),
		Speed:              binary.BigEndian.Uint32(b[o+8:]),
		IDVendor:           binary.BigEndian.Uint16(b[o+12:]),
		IDProduct:          binary.BigEndian.Uint16(b[o+14:]),
		BCDDevice:          binary.BigEndian.Uint16(b[o+16:]),
		DeviceClass:        b[o+18],
		DeviceSubClass:     b[o+19],
		DeviceProtocol:     b[o+20],
		ConfigurationValue: b[o+21],
		NumConfigurations:  b[o+22],
		NumInterfaces:      b[o+23],
	}
}

// MarshalDevlistReply encodes OP_REP_DEVLIST.
func MarshalDevlistReply(devs []DeviceInfo) []byte {
	b := OpHeader{Version, OpRepDevlist, 0}.Marshal()
	b = binary.BigEndian.AppendUint32(b, uint32(len(devs)))
	for i := range devs {
		b = append(b, devs[i].marshal(true)...)
	}
	return b
}

// MarshalImportReply encodes OP_REP_IMPORT. A nil dev means failure
// (status 1, no device record).
func MarshalImportReply(dev *DeviceInfo) []byte {
	if dev == nil {
		return OpHeader{Version, OpRepImport, 1}.Marshal()
	}
	return append(OpHeader{Version, OpRepImport, 0}.Marshal(), dev.marshal(false)...)
}

// MarshalImportRequest encodes OP_REQ_IMPORT (the client side; used by
// tests and the in-process attach).
func MarshalImportRequest(busID string) []byte {
	b := OpHeader{Version, OpReqImport, 0}.Marshal()
	var id [32]byte
	copy(id[:31], busID)
	return append(b, id[:]...)
}

// ReadImportReply reads OP_REP_IMPORT after its header has been read.
func ReadImportReply(r io.Reader, h OpHeader) (*DeviceInfo, error) {
	if h.Code != OpRepImport {
		return nil, fmt.Errorf("usbip: unexpected reply code %#04x", h.Code)
	}
	if h.Status != 0 {
		return nil, fmt.Errorf("usbip: import failed with status %d", h.Status)
	}
	b := make([]byte, deviceInfoLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	d := unmarshalDeviceInfo(b)
	return &d, nil
}

// ReadDevlistReply reads OP_REP_DEVLIST after its header has been read.
func ReadDevlistReply(r io.Reader) ([]DeviceInfo, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	count := binary.BigEndian.Uint32(n[:])
	if count > 1024 {
		return nil, fmt.Errorf("usbip: implausible device count %d", count)
	}
	var out []DeviceInfo
	for range count {
		b := make([]byte, deviceInfoLen)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		d := unmarshalDeviceInfo(b)
		for range d.NumInterfaces {
			var it [4]byte
			if _, err := io.ReadFull(r, it[:]); err != nil {
				return nil, err
			}
			d.Interfaces = append(d.Interfaces, Interface{it[0], it[1], it[2]})
		}
		out = append(out, d)
	}
	return out, nil
}

// ReadBusID reads the 32-byte busid that follows an OP_REQ_IMPORT header.
func ReadBusID(r io.Reader) (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return cstr(b[:]), nil
}

// IsoPacket is one isochronous packet descriptor (16 bytes on the wire).
type IsoPacket struct {
	Offset       uint32
	Length       uint32
	ActualLength uint32
	Status       int32
}

const isoPacketLen = 16

// Header is the common 20-byte URB header plus the 28-byte
// command-specific part, flattened. Which fields are meaningful depends
// on Command.
type Header struct {
	Command   uint32
	SeqNum    uint32
	DevID     uint32
	Direction uint32
	EP        uint32

	// CMD_SUBMIT
	TransferFlags        uint32
	TransferBufferLength int32
	StartFrame           int32
	NumberOfPackets      uint32 // NoPackets (or 0) for non-iso
	Interval             int32
	Setup                [8]byte

	// RET_SUBMIT / RET_UNLINK
	Status       int32
	ActualLength int32
	ErrorCount   int32

	// CMD_UNLINK
	UnlinkSeqNum uint32
}

const headerLen = 48

// IsIso reports whether a CMD_SUBMIT carries isochronous packets.
func (h *Header) IsIso() bool {
	return h.NumberOfPackets != 0 && h.NumberOfPackets != NoPackets
}

// MarshalHeader encodes h into its 48-byte wire form.
func (h *Header) Marshal() []byte {
	b := make([]byte, headerLen)
	be := binary.BigEndian
	be.PutUint32(b[0:], h.Command)
	be.PutUint32(b[4:], h.SeqNum)
	be.PutUint32(b[8:], h.DevID)
	be.PutUint32(b[12:], h.Direction)
	be.PutUint32(b[16:], h.EP)
	switch h.Command {
	case CmdSubmit:
		be.PutUint32(b[20:], h.TransferFlags)
		be.PutUint32(b[24:], uint32(h.TransferBufferLength))
		be.PutUint32(b[28:], uint32(h.StartFrame))
		be.PutUint32(b[32:], h.NumberOfPackets)
		be.PutUint32(b[36:], uint32(h.Interval))
		copy(b[40:], h.Setup[:])
	case RetSubmit:
		be.PutUint32(b[20:], uint32(h.Status))
		be.PutUint32(b[24:], uint32(h.ActualLength))
		be.PutUint32(b[28:], uint32(h.StartFrame))
		be.PutUint32(b[32:], h.NumberOfPackets)
		be.PutUint32(b[36:], uint32(h.ErrorCount))
	case CmdUnlink:
		be.PutUint32(b[20:], h.UnlinkSeqNum)
	case RetUnlink:
		be.PutUint32(b[20:], uint32(h.Status))
	}
	return b
}

// UnmarshalHeader decodes a 48-byte URB header.
func UnmarshalHeader(b []byte) (Header, error) {
	if len(b) < headerLen {
		return Header{}, io.ErrUnexpectedEOF
	}
	be := binary.BigEndian
	h := Header{
		Command:   be.Uint32(b[0:]),
		SeqNum:    be.Uint32(b[4:]),
		DevID:     be.Uint32(b[8:]),
		Direction: be.Uint32(b[12:]),
		EP:        be.Uint32(b[16:]),
	}
	switch h.Command {
	case CmdSubmit:
		h.TransferFlags = be.Uint32(b[20:])
		h.TransferBufferLength = int32(be.Uint32(b[24:]))
		h.StartFrame = int32(be.Uint32(b[28:]))
		h.NumberOfPackets = be.Uint32(b[32:])
		h.Interval = int32(be.Uint32(b[36:]))
		copy(h.Setup[:], b[40:48])
	case RetSubmit:
		h.Status = int32(be.Uint32(b[20:]))
		h.ActualLength = int32(be.Uint32(b[24:]))
		h.StartFrame = int32(be.Uint32(b[28:]))
		h.NumberOfPackets = be.Uint32(b[32:])
		h.ErrorCount = int32(be.Uint32(b[36:]))
	case CmdUnlink:
		h.UnlinkSeqNum = be.Uint32(b[20:])
	case RetUnlink:
		h.Status = int32(be.Uint32(b[20:]))
	default:
		return h, fmt.Errorf("usbip: unknown command %#x", h.Command)
	}
	return h, nil
}

// maxTransfer bounds buffer sizes read off the wire.
const maxTransfer = 16 << 20

// maxIsoPackets bounds number_of_packets read off the wire.
const maxIsoPackets = 1024

var errTooLarge = errors.New("usbip: transfer too large")

// Message is one URB-phase message: a header plus its payload. For
// CMD_SUBMIT OUT and RET_SUBMIT IN, Data is the transfer buffer; Iso holds
// the isochronous packet descriptors that follow it.
type Message struct {
	Header
	Data []byte
	Iso  []IsoPacket
}

// Marshal encodes m, including payload and iso descriptors.
func (m *Message) Marshal() []byte {
	b := m.Header.Marshal()
	b = append(b, m.Data...)
	for _, p := range m.Iso {
		b = binary.BigEndian.AppendUint32(b, p.Offset)
		b = binary.BigEndian.AppendUint32(b, p.Length)
		b = binary.BigEndian.AppendUint32(b, p.ActualLength)
		b = binary.BigEndian.AppendUint32(b, uint32(p.Status))
	}
	return b
}

// ReadMessage reads one URB-phase message. Payload rules (from the spec):
// CMD_SUBMIT carries transfer_buffer_length bytes only for OUT; RET_SUBMIT
// carries actual_length bytes only for IN. Either is followed by
// number_of_packets iso descriptors when it is isochronous.
//
// RET_SUBMIT's direction field is 0 on the wire, so a client must say
// which direction the URB with a given seqnum had: retDir does that. It
// may be nil when reading commands.
func ReadMessage(r io.Reader, retDir func(seqnum uint32) uint32) (*Message, error) {
	var hb [headerLen]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		return nil, err
	}
	h, err := UnmarshalHeader(hb[:])
	if err != nil {
		return nil, err
	}
	m := &Message{Header: h}
	var n int32
	switch {
	case h.Command == CmdSubmit && h.Direction == DirOut:
		n = h.TransferBufferLength
	case h.Command == RetSubmit && retDir != nil && retDir(h.SeqNum) == DirIn:
		n = h.ActualLength
	}
	if n < 0 || n > maxTransfer {
		return nil, errTooLarge
	}
	if n > 0 {
		m.Data = make([]byte, n)
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return nil, err
		}
	}
	if (h.Command == CmdSubmit || h.Command == RetSubmit) && h.IsIso() {
		if h.NumberOfPackets > maxIsoPackets {
			return nil, errTooLarge
		}
		buf := make([]byte, isoPacketLen*int(h.NumberOfPackets))
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		m.Iso = make([]IsoPacket, h.NumberOfPackets)
		for i := range m.Iso {
			p := buf[i*isoPacketLen:]
			m.Iso[i] = IsoPacket{
				Offset:       binary.BigEndian.Uint32(p[0:]),
				Length:       binary.BigEndian.Uint32(p[4:]),
				ActualLength: binary.BigEndian.Uint32(p[8:]),
				Status:       int32(binary.BigEndian.Uint32(p[12:])),
			}
		}
	}
	return m, nil
}
