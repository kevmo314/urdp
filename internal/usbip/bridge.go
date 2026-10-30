package usbip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Attacher plugs a device into the host's USB stack.
type Attacher interface {
	// Attach makes dev appear on the host and returns the URB-phase
	// USB/IP stream (CMD_SUBMIT in, RET_SUBMIT out) the host speaks.
	Attach(dev *Device) (Attachment, error)
}

// Attachment is an attached device. Closing it detaches the device.
type Attachment interface {
	io.ReadWriteCloser
	String() string // e.g. "vhci_hcd port 2"
}

// Bridge connects one browser data channel to one attached device: it
// turns the host's URBs into WebUSB operations and the browser's results
// back into URB completions.
type Bridge struct {
	send     func([]byte) error // sends one data channel message
	closeCh  func()             // closes the data channel
	attacher Attacher
	busID    string
	Logf     func(format string, args ...any)

	smu    sync.Mutex // keeps framed messages from interleaving
	framer Framer

	mu      sync.Mutex // guards everything below, and writes to att
	dev     *Device
	att     Attachment
	closed  bool
	nextID  uint32
	ops     map[uint32]*urb  // in flight in the browser, by op id
	seqs    map[uint32]*urb  // URBs the host is waiting for, by seqnum
	orphans map[uint8][]*urb // unlinked IN transfers, by endpoint
}

// urb is a host URB and the browser operation serving it.
type urb struct {
	id       uint32
	hdr      Header // the CMD_SUBMIT this result answers
	iso      []IsoPacket
	ep       uint8 // endpoint address, 0x80 set for IN
	unlinked bool
	done     *Done // result of an orphan nobody has claimed yet
}

// NewBridge returns a bridge for a device with the given USB/IP bus id.
// send transmits one data channel message; closeCh closes the channel.
func NewBridge(attacher Attacher, busID string, send func([]byte) error, closeCh func()) *Bridge {
	return &Bridge{
		send:     send,
		closeCh:  closeCh,
		attacher: attacher,
		busID:    busID,
		Logf:     func(string, ...any) {},
		ops:      map[uint32]*urb{},
		seqs:     map[uint32]*urb{},
		orphans:  map[uint8][]*urb{},
	}
}

// Open greets the browser; call it once the channel is open.
func (b *Bridge) Open() { b.sendMsg(StatusMsg(StatusReady, "")) }

// Reject tells the browser USB passthrough is unavailable.
func Reject(send func([]byte) error, reason string) {
	for _, c := range Frame(StatusMsg(StatusError, reason)) {
		_ = send(c)
	}
}

func (b *Bridge) sendMsg(msg []byte) {
	b.smu.Lock()
	defer b.smu.Unlock()
	for _, c := range Frame(msg) {
		if err := b.send(c); err != nil {
			return
		}
	}
}

func (b *Bridge) fail(err error) {
	b.Logf("usb %s: %v", b.busID, err)
	b.sendMsg(StatusMsg(StatusError, err.Error()))
	b.Close()
	b.closeCh()
}

// HandleMessage processes one data channel message from the browser.
// Messages must be passed in order, from one goroutine.
func (b *Bridge) HandleMessage(chunk []byte) {
	if err := b.framer.Feed(chunk, b.handle); err != nil {
		b.fail(err)
	}
}

func (b *Bridge) handle(msg []byte) {
	if len(msg) == 0 {
		return
	}
	switch msg[0] {
	case MsgHello:
		h, err := UnmarshalHello(msg)
		if err != nil {
			b.fail(err)
			return
		}
		b.attach(h)
	case MsgDone:
		d, err := UnmarshalDone(msg)
		if err != nil {
			b.fail(err)
			return
		}
		b.complete(d)
	default:
		b.fail(fmt.Errorf("unexpected message type %#x", msg[0]))
	}
}

func (b *Bridge) attach(h *Hello) {
	b.mu.Lock()
	if b.dev != nil || b.closed {
		b.mu.Unlock()
		b.fail(errors.New("duplicate hello"))
		return
	}
	dev, err := NewDevice(h, b.busID)
	if err != nil {
		b.mu.Unlock()
		b.fail(err)
		return
	}
	b.dev = dev
	b.mu.Unlock()

	// Attach outside the lock: the host starts enumerating at once, and
	// its URBs are read by readHost below.
	att, err := b.attacher.Attach(dev)
	if err != nil {
		b.fail(fmt.Errorf("attach %04x:%04x: %w", dev.VendorID, dev.ProductID, err))
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		att.Close()
		return
	}
	b.att = att
	b.mu.Unlock()
	b.Logf("usb %s: attached %04x:%04x %q (speed %d) as %s", b.busID, dev.VendorID, dev.ProductID, dev.Name, dev.Speed, att)
	b.sendMsg(StatusMsg(StatusAttached, att.String()))
	go b.readHost(att)
}

// Close detaches the device. In-flight browser operations are abandoned.
func (b *Bridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	att := b.att
	b.mu.Unlock()
	if att != nil {
		if err := att.Close(); err != nil {
			b.Logf("usb %s: detach: %v", b.busID, err)
		} else {
			b.Logf("usb %s: detached", b.busID)
		}
	}
}

func (b *Bridge) readHost(att Attachment) {
	for {
		m, err := ReadMessage(att, nil)
		if err != nil {
			b.mu.Lock()
			closed := b.closed
			b.mu.Unlock()
			if !closed {
				if errors.Is(err, io.EOF) {
					err = errors.New("host detached the device")
				}
				b.fail(err)
			}
			return
		}
		switch m.Command {
		case CmdSubmit:
			b.submit(m)
		case CmdUnlink:
			b.unlink(m)
		default:
			b.fail(fmt.Errorf("unexpected command %#x from host", m.Command))
			return
		}
	}
}

// Standard control requests (USB 2.0 §9.4).
const (
	reqClearFeature  = 1
	reqSetFeature    = 3
	reqSetConfig     = 9
	reqSetInterface  = 11
	featEndpointHalt = 0
	featPortReset    = 4
)

// controlOp maps a control URB onto a browser operation. Chrome refuses
// standard OUT requests sent with controlTransferOut, so the ones that
// change device state are mapped onto their dedicated WebUSB calls (as
// the kernel's own USB/IP host driver does with usb_set_interface etc.),
// and the rest are acknowledged locally.
func controlOp(m *Message) (op Op, local bool) {
	s := m.Setup
	reqType, req := s[0], s[1]
	value := binary.LittleEndian.Uint16(s[2:])
	index := binary.LittleEndian.Uint16(s[4:])
	switch {
	case reqType == 0x00 && req == reqSetConfig:
		return Op{Type: MsgSetConf, Value: uint8(value)}, false
	case reqType == 0x01 && req == reqSetInterface:
		return Op{Type: MsgSetAlt, Value: uint8(index), Alt: uint8(value)}, false
	case reqType == 0x02 && req == reqClearFeature && value == featEndpointHalt:
		return Op{Type: MsgClear, EP: uint8(index)}, false
	case reqType == 0x23 && req == reqSetFeature && value == featPortReset:
		// What the Linux USB/IP host driver treats as "reset the device".
		return Op{Type: MsgReset}, false
	case reqType&0xe0 == 0x00 && reqType&0x80 == 0:
		// Other standard OUT requests (SET_FEATURE remote wakeup,
		// SET_SEL, SET_ISOCH_DELAY, ...): WebUSB can't issue them.
		return Op{}, true
	}
	return Op{Type: MsgControl, Setup: s, Data: m.Data}, false
}

func (b *Bridge) submit(m *Message) {
	epn := uint8(m.EP & 0x0f)
	if m.Direction == DirIn {
		epn |= 0x80
	}
	u := &urb{hdr: m.Header, iso: m.Iso, ep: epn}
	var op Op
	switch {
	case m.EP == 0:
		var local bool
		if op, local = controlOp(m); local {
			b.mu.Lock()
			b.retSubmit(u, &Done{Actual: uint32(len(m.Data))})
			b.mu.Unlock()
			return
		}
	case m.IsIso():
		op = Op{Type: MsgIso, EP: epn, Iso: make([]uint32, len(m.Iso))}
		for i, p := range m.Iso {
			op.Iso[i] = p.Length
			if m.Direction == DirOut {
				// WebUSB wants the packets back to back.
				end := min(uint64(p.Offset)+uint64(p.Length), uint64(len(m.Data)))
				if uint64(p.Offset) < end {
					op.Data = append(op.Data, m.Data[p.Offset:end]...)
				}
			}
		}
	default:
		op = Op{Type: MsgTransfer, EP: epn, Data: m.Data}
		if m.Direction == DirIn {
			op.Length = uint32(max(m.TransferBufferLength, 0))
		} else if m.TransferFlags&URBZeroPacket != 0 && len(m.Data) > 0 {
			b.mu.Lock()
			mp := b.dev.maxPacket[epn]
			b.mu.Unlock()
			if mp > 0 && len(m.Data)%mp == 0 {
				op.Flags |= TransferZLP
			}
		}
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if op.Type == MsgTransfer && epn&0x80 != 0 {
		if q := b.orphans[epn]; len(q) > 0 {
			// An earlier read on this endpoint was cancelled by the host,
			// but WebUSB can't cancel it. Like a real controller that
			// stopped polling, hand whatever it reads to this URB instead.
			o := q[0]
			b.orphans[epn] = q[1:]
			o.hdr, o.unlinked = m.Header, false
			if o.done != nil {
				d := o.done
				o.done = nil
				b.retSubmit(o, d)
			} else {
				b.seqs[m.SeqNum] = o
			}
			b.mu.Unlock()
			return
		}
	}
	b.nextID++
	u.id = b.nextID
	op.ID = u.id
	b.ops[u.id] = u
	b.seqs[m.SeqNum] = u
	b.mu.Unlock()
	b.sendMsg(op.Marshal())
}

func (b *Bridge) unlink(m *Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := int32(StatusOK) // already completed
	if u := b.seqs[m.UnlinkSeqNum]; u != nil {
		delete(b.seqs, m.UnlinkSeqNum)
		u.unlinked = true
		status = StatusECONNRESET
		if u.ep&0x80 != 0 && u.ep != 0x80 && u.iso == nil {
			b.orphans[u.ep] = append(b.orphans[u.ep], u)
		}
	}
	b.write(&Message{Header: Header{Command: RetUnlink, SeqNum: m.SeqNum, Status: status}})
}

func (b *Bridge) complete(d *Done) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.ops[d.ID]
	if u == nil {
		return
	}
	delete(b.ops, d.ID)
	if u.unlinked {
		// An orphan: keep a successful read for the next URB on its
		// endpoint; drop anything else.
		q := b.orphans[u.ep]
		for i, o := range q {
			if o == u {
				if d.Status == StatusOK {
					u.done = d
				} else {
					b.orphans[u.ep] = append(q[:i:i], q[i+1:]...)
				}
				break
			}
		}
		return
	}
	if b.seqs[u.hdr.SeqNum] == u {
		delete(b.seqs, u.hdr.SeqNum)
	}
	if u.hdr.EP == 0 && d.Status == StatusOK && b.dev != nil {
		if s := u.hdr.Setup; s[0] == 0x00 && s[1] == reqSetConfig {
			b.dev.ActiveConfig = s[2]
		}
	}
	b.retSubmit(u, d)
}

// retSubmit writes the RET_SUBMIT for u. b.mu must be held, so that a
// RET_UNLINK can never overtake the RET_SUBMIT it refers to.
func (b *Bridge) retSubmit(u *urb, d *Done) {
	h := &u.hdr
	r := Message{Header: Header{
		Command:         RetSubmit,
		SeqNum:          h.SeqNum,
		Status:          d.Status,
		StartFrame:      h.StartFrame,
		NumberOfPackets: h.NumberOfPackets,
	}}
	in := h.Direction == DirIn
	switch {
	case h.IsIso():
		r.Iso = make([]IsoPacket, len(u.iso))
		var total uint32
		for i, p := range u.iso {
			r.Iso[i] = IsoPacket{Offset: p.Offset, Length: p.Length, Status: StatusEPROTO}
			if i < len(d.Packets) {
				r.Iso[i].Status = d.Packets[i].Status
				r.Iso[i].ActualLength = min(d.Packets[i].Actual, p.Length)
			}
			if r.Iso[i].Status != StatusOK {
				r.ErrorCount++
			}
			total += r.Iso[i].ActualLength
		}
		if in {
			// The packed data must add up to the packets' actual lengths.
			data := d.Data
			if uint32(len(data)) < total {
				data = append(data, make([]byte, total-uint32(len(data)))...)
			}
			r.Data = data[:total]
		}
		r.ActualLength = int32(total)
	case in:
		data := d.Data
		if want := int(max(h.TransferBufferLength, 0)); len(data) > want {
			data = data[:want]
			if r.Status == StatusOK {
				r.Status = StatusEOVERFLOW
			}
		}
		r.Data = data
		r.ActualLength = int32(len(data))
		if r.Status == StatusOK && h.TransferFlags&URBShortNotOK != 0 && r.ActualLength < h.TransferBufferLength {
			r.Status = StatusEREMOTEIO
		}
	default:
		r.ActualLength = int32(min(d.Actual, uint32(max(h.TransferBufferLength, 0))))
	}
	b.write(&r)
}

// write sends a message to the host. b.mu must be held.
func (b *Bridge) write(m *Message) {
	if b.att == nil || b.closed {
		return
	}
	if _, err := b.att.Write(m.Marshal()); err != nil {
		b.Logf("usb %s: write to host: %v", b.busID, err)
	}
}
