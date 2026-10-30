package usbip

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeAttacher "attaches" to a test host over net.Pipe.
type pipeAttacher struct{ host chan net.Conn }

type pipeAttachment struct{ net.Conn }

func (pipeAttachment) String() string { return "pipe" }

func (p *pipeAttacher) Attach(*Device) (Attachment, error) {
	a, b := net.Pipe()
	p.host <- b
	return pipeAttachment{a}, nil
}

// testHost plays the kernel's vhci-hcd side of the URB phase.
type testHost struct {
	t    *testing.T
	conn net.Conn
	mu   sync.Mutex
	seq  uint32
	dirs map[uint32]uint32
	rets chan *Message
}

func newTestHost(t *testing.T) (*testHost, *fakeBrowser) {
	pa := &pipeAttacher{host: make(chan net.Conn, 1)}
	f := newFakeBrowser(t, pa)
	f.start()
	h := &testHost{t: t, conn: <-pa.host, dirs: map[uint32]uint32{}, rets: make(chan *Message, 64)}
	f.waitStatus(StatusAttached)
	go func() {
		for {
			m, err := ReadMessage(h.conn, func(seq uint32) uint32 {
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.dirs[seq]
			})
			if err != nil {
				close(h.rets)
				return
			}
			h.rets <- m
		}
	}()
	t.Cleanup(func() { f.b.Close(); h.conn.Close() })
	return h, f
}

func (h *testHost) submit(m Message) uint32 {
	h.mu.Lock()
	h.seq++
	m.Command, m.SeqNum, m.DevID = CmdSubmit, h.seq, 0x10001
	if m.NumberOfPackets == 0 {
		m.NumberOfPackets = 0
	}
	if m.Direction == DirOut {
		m.TransferBufferLength = int32(len(m.Data))
	}
	h.dirs[m.SeqNum] = m.Direction
	h.mu.Unlock()
	if _, err := h.conn.Write(m.Marshal()); err != nil {
		h.t.Fatal(err)
	}
	return m.SeqNum
}

func (h *testHost) control(setup [8]byte, data []byte) uint32 {
	dir := uint32(DirOut)
	if setup[0]&0x80 != 0 {
		dir = DirIn
	}
	m := Message{Header: Header{Direction: dir, EP: 0, Setup: setup}, Data: data}
	if dir == DirIn {
		m.TransferBufferLength = int32(binary.LittleEndian.Uint16(setup[6:]))
	}
	return h.submit(m)
}

func (h *testHost) bulkIn(ep uint32, n int32, flags uint32) uint32 {
	return h.submit(Message{Header: Header{Direction: DirIn, EP: ep, TransferBufferLength: n, TransferFlags: flags}})
}

func (h *testHost) bulkOut(ep uint32, data []byte, flags uint32) uint32 {
	return h.submit(Message{Header: Header{Direction: DirOut, EP: ep, TransferFlags: flags}, Data: data})
}

func (h *testHost) unlink(seq uint32) uint32 {
	h.mu.Lock()
	h.seq++
	m := Message{Header: Header{Command: CmdUnlink, SeqNum: h.seq, DevID: 0x10001, UnlinkSeqNum: seq}}
	h.mu.Unlock()
	if _, err := h.conn.Write(m.Marshal()); err != nil {
		h.t.Fatal(err)
	}
	return m.SeqNum
}

func (h *testHost) expect(seq uint32) *Message {
	h.t.Helper()
	select {
	case m, ok := <-h.rets:
		if !ok {
			h.t.Fatal("host connection closed")
		}
		if m.SeqNum != seq {
			h.t.Fatalf("got reply for seq %d (%+v), want %d", m.SeqNum, m.Header, seq)
		}
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatalf("no reply for seq %d", seq)
	}
	return nil
}

func (h *testHost) expectNothing() {
	h.t.Helper()
	select {
	case m := <-h.rets:
		h.t.Fatalf("unexpected reply %+v", m.Header)
	case <-time.After(50 * time.Millisecond):
	}
}

func setup(reqType, req uint8, value, index, length uint16) [8]byte {
	var s [8]byte
	s[0], s[1] = reqType, req
	binary.LittleEndian.PutUint16(s[2:], value)
	binary.LittleEndian.PutUint16(s[4:], index)
	binary.LittleEndian.PutUint16(s[6:], length)
	return s
}

func TestBridgeControl(t *testing.T) {
	h, f := newTestHost(t)

	r := h.expect(h.control(setup(0x80, 6, 0x0100, 0, 64), nil))
	if r.Command != RetSubmit || r.Status != 0 || !bytes.Equal(r.Data, fakeDeviceDesc) || r.ActualLength != 18 {
		t.Fatalf("device descriptor: %+v % x", r.Header, r.Data)
	}
	if r.DevID != 0 || r.Direction != 0 || r.EP != 0 {
		t.Fatalf("RET_SUBMIT basic header must be zero: %+v", r.Header)
	}
	// Unknown descriptor: stall.
	if r := h.expect(h.control(setup(0x80, 6, 0x0600, 0, 10), nil)); r.Status != StatusEPIPE {
		t.Fatalf("qualifier: status %d", r.Status)
	}
	// Class OUT with data, then read it back.
	coding := []byte{0x00, 0xc2, 0x01, 0x00, 0, 0, 8} // 115200 8N1
	if r := h.expect(h.control(setup(0x21, 0x20, 0, 0, 7), coding)); r.Status != 0 || r.ActualLength != 7 || r.Data != nil {
		t.Fatalf("set line coding: %+v", r.Header)
	}
	if r := h.expect(h.control(setup(0xa1, 0x21, 0, 0, 7), nil)); !bytes.Equal(r.Data, coding) {
		t.Fatalf("get line coding: % x", r.Data)
	}

	// State-changing standard requests become dedicated WebUSB calls.
	h.expect(h.control(setup(0x00, 9, 1, 0, 0), nil))
	h.expect(h.control(setup(0x01, 11, 0, 1, 0), nil))
	h.expect(h.control(setup(0x02, 1, 0, 0x83, 0), nil))
	h.expect(h.control(setup(0x23, 3, 4, 1, 0), nil))
	// Other standard OUT requests are acknowledged without the browser.
	if r := h.expect(h.control(setup(0x00, 3, 1, 0, 0), nil)); r.Status != 0 {
		t.Fatalf("remote wakeup: %d", r.Status)
	}
	if c := f.opsOf(MsgSetConf); len(c) != 1 || c[0].Value != 1 {
		t.Fatalf("set config ops %+v", c)
	}
	if a := f.opsOf(MsgSetAlt); len(a) != 1 || a[0].Value != 1 || a[0].Alt != 0 {
		t.Fatalf("set alt ops %+v", a)
	}
	if c := f.opsOf(MsgClear); len(c) != 1 || c[0].EP != 0x83 {
		t.Fatalf("clear halt ops %+v", c)
	}
	if len(f.opsOf(MsgReset)) != 1 {
		t.Fatal("no reset op")
	}
	for _, o := range f.opsOf(MsgControl) {
		if o.Setup[0]&0x60 == 0 && o.Setup[0]&0x80 == 0 {
			t.Fatalf("standard OUT request reached the browser: % x", o.Setup)
		}
	}
}

func TestBridgeBulkEcho(t *testing.T) {
	h, f := newTestHost(t)
	in := h.bulkIn(3, 64, 0)
	h.expectNothing() // pending until data arrives, like WebUSB
	out := h.bulkOut(2, []byte("hello"), 0)
	// Order between the two replies depends on the browser; collect both.
	got := map[uint32]*Message{}
	for range 2 {
		m := <-h.rets
		got[m.SeqNum] = m
	}
	if got[out].ActualLength != 5 || got[out].Data != nil {
		t.Fatalf("out: %+v", got[out].Header)
	}
	if string(got[in].Data) != "hello" || got[in].ActualLength != 5 {
		t.Fatalf("in: %+v %q", got[in].Header, got[in].Data)
	}

	// Transfers larger than a data channel chunk.
	big := bytes.Repeat([]byte("0123456789abcdef"), 10000)
	if r := h.expect(h.bulkOut(2, big, 0)); r.ActualLength != int32(len(big)) {
		t.Fatalf("big out actual %d", r.ActualLength)
	}
	if o := f.opsOf(MsgTransfer); !bytes.Equal(o[len(o)-1].Data, big) {
		t.Fatal("big out payload corrupted")
	}

	// URB_SHORT_NOT_OK turns a short read into -EREMOTEIO.
	f.mu.Lock()
	f.echo = nil
	f.mu.Unlock()
	h.expect(h.bulkOut(2, []byte("xy"), 0))
	if r := h.expect(h.bulkIn(3, 64, URBShortNotOK)); r.Status != StatusEREMOTEIO || string(r.Data) != "xy" {
		t.Fatalf("short not ok: %+v", r.Header)
	}
}

func TestBridgeZeroPacket(t *testing.T) {
	h, f := newTestHost(t)
	h.expect(h.bulkOut(2, make([]byte, 128), URBZeroPacket))
	h.expect(h.bulkOut(2, make([]byte, 100), URBZeroPacket))
	h.expect(h.bulkOut(2, make([]byte, 128), 0))
	o := f.opsOf(MsgTransfer)
	if o[0].Flags != TransferZLP || o[1].Flags != 0 || o[2].Flags != 0 {
		t.Fatalf("flags %d %d %d", o[0].Flags, o[1].Flags, o[2].Flags)
	}
}

func TestBridgeUnlink(t *testing.T) {
	h, f := newTestHost(t)

	// Unlinking a read WebUSB can't cancel: answered at once.
	in := h.bulkIn(3, 64, 0)
	u := h.unlink(in)
	if r := h.expect(u); r.Command != RetUnlink || r.Status != StatusECONNRESET {
		t.Fatalf("unlink: %+v", r.Header)
	}
	// Data the abandoned read picks up goes to the next read on that
	// endpoint, which reuses the abandoned browser transfer.
	out := h.bulkOut(2, []byte("abc"), 0)
	h.expect(out)
	time.Sleep(20 * time.Millisecond) // let the orphan complete first
	in2 := h.bulkIn(3, 64, 0)
	if r := h.expect(in2); string(r.Data) != "abc" {
		t.Fatalf("adopted read: %+v %q", r.Header, r.Data)
	}
	// Still in flight when adopted.
	in3 := h.bulkIn(3, 64, 0)
	h.expect(h.unlink(in3))
	in4 := h.bulkIn(3, 2, 0)
	h.expectNothing()
	out = h.bulkOut(2, []byte("defg"), 0)
	got := map[uint32]*Message{}
	for range 2 {
		m := <-h.rets
		got[m.SeqNum] = m
	}
	if r := got[in4]; r == nil || got[out] == nil || r.Status != StatusEOVERFLOW || string(r.Data) != "de" {
		t.Fatalf("adopted overflow: %+v", got)
	}
	if n := len(f.opsOf(MsgTransfer)); n != 4 { // 2 reads + 2 writes
		t.Fatalf("%d transfer ops, want 4", n)
	}

	// Unlinking something already completed: status 0.
	done := h.control(setup(0x80, 0, 0, 0, 2), nil)
	h.expect(done)
	if r := h.expect(h.unlink(done)); r.Status != 0 {
		t.Fatalf("late unlink status %d", r.Status)
	}
	// An unlinked interrupt read that never completes doesn't wedge
	// anything; no RET_SUBMIT may follow its RET_UNLINK.
	irq := h.bulkIn(1, 8, 0)
	h.expect(h.unlink(irq))
	h.expectNothing()
}

func TestBridgeIso(t *testing.T) {
	h, f := newTestHost(t)
	// IN: three packets of 4 bytes; the fake returns 1, 2, 3 bytes.
	seq := h.submit(Message{
		Header: Header{Direction: DirIn, EP: 4, TransferBufferLength: 12, NumberOfPackets: 3, StartFrame: 5},
		Iso:    []IsoPacket{{0, 4, 0, 0}, {4, 4, 0, 0}, {8, 4, 0, 0}},
	})
	r := h.expect(seq)
	wantIso := []IsoPacket{{0, 4, 1, 0}, {4, 4, 2, 0}, {8, 4, 3, 0}}
	if r.ActualLength != 6 || !bytes.Equal(r.Data, []byte{0, 1, 1, 2, 2, 2}) || r.NumberOfPackets != 3 || r.ErrorCount != 0 {
		t.Fatalf("iso in: %+v % x", r.Header, r.Data)
	}
	for i := range wantIso {
		if r.Iso[i] != wantIso[i] {
			t.Fatalf("packet %d: %+v", i, r.Iso[i])
		}
	}
	// OUT: packets at non-contiguous offsets are packed for WebUSB.
	seq = h.submit(Message{
		Header: Header{Direction: DirOut, EP: 4, NumberOfPackets: 2},
		Data:   []byte{1, 2, 0, 0, 3, 4},
		Iso:    []IsoPacket{{0, 2, 0, 0}, {4, 2, 0, 0}},
	})
	r = h.expect(seq)
	if r.ActualLength != 4 || r.Iso[1].ActualLength != 2 {
		t.Fatalf("iso out: %+v", r)
	}
	if o := f.opsOf(MsgIso); !bytes.Equal(o[1].Data, []byte{1, 2, 3, 4}) || o[1].Iso[0] != 2 {
		t.Fatalf("iso out op %+v", o[1])
	}
	// Failed packets count as errors.
	seq = h.submit(Message{Header: Header{Direction: DirIn, EP: 5, TransferBufferLength: 8, NumberOfPackets: 2},
		Iso: []IsoPacket{{0, 4, 0, 0}, {4, 4, 0, 0}}})
	if r := h.expect(seq); r.ErrorCount != 2 || r.Iso[0].Status != StatusEPROTO || r.ActualLength != 0 {
		t.Fatalf("iso errors: %+v", r)
	}
}

func TestBridgeCloseDetaches(t *testing.T) {
	h, f := newTestHost(t)
	h.bulkIn(3, 64, 0)
	f.b.Close()
	select {
	case _, ok := <-h.rets:
		if ok {
			t.Fatal("unexpected message")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host connection not closed")
	}
}

func TestBridgeHostDetach(t *testing.T) {
	h, f := newTestHost(t)
	h.conn.Close()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after host detach")
	}
}

type failAttacher struct{}

func (failAttacher) Attach(*Device) (Attachment, error) { return nil, io.ErrClosedPipe }

func TestBridgeAttachError(t *testing.T) {
	f := newFakeBrowser(t, failAttacher{})
	f.start()
	select {
	case m := <-f.status:
		if m[1] != StatusReady {
			t.Fatalf("first status %d", m[1])
		}
	case <-time.After(time.Second):
		t.Fatal("no ready")
	}
	select {
	case m := <-f.status:
		if m[1] != StatusError || !bytes.Contains(m, []byte("1209:0001")) {
			t.Fatalf("status %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no error")
	}
	<-f.closed
}
