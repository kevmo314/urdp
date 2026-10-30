package usbip

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeBrowser stands in for web/assets/usb.js plus WebUSB: it answers the
// data channel protocol with a fake CDC-ACM device (pid.codes test VID/PID
// 1209:0001) that echoes whatever is written to its bulk OUT endpoint back
// on its bulk IN endpoint. Like WebUSB, an IN transfer with nothing to
// read stays pending until data arrives, and nothing can be cancelled.
type fakeBrowser struct {
	t      *testing.T
	b      *Bridge
	framer Framer
	out    chan []byte // chunks to deliver to the bridge, in order

	status chan []byte // MsgStatus messages received
	closed chan struct{}

	dmu      sync.Mutex
	mu       sync.Mutex
	ops      []*Op
	echo     []byte
	pending  []*Op // bulk IN reads waiting for data
	parked   []*Op // interrupt IN reads that never complete
	coding   []byte
	config   uint8
	inFlight map[uint32]bool
}

const (
	fakeVID = 0x1209
	fakePID = 0x0001
)

var fakeDeviceDesc = []byte{
	18, 1, 0x00, 0x02, // bcdUSB 2.00
	0x02, 0, 0, 64, // class CDC, ep0 64 bytes
	0x09, 0x12, 0x01, 0x00, // 1209:0001
	0x00, 0x01, 1, 2, 3, 1, // bcdDevice 1.00, strings, 1 config
}

var fakeConfigDesc = []byte{
	9, 2, 67, 0, 2, 1, 0, 0x80, 50,
	// interface 0: CDC communication, ACM
	9, 4, 0, 0, 1, 0x02, 0x02, 0x01, 0,
	5, 0x24, 0x00, 0x10, 0x01, // header
	5, 0x24, 0x01, 0x00, 0x01, // call management
	4, 0x24, 0x02, 0x02, // ACM
	5, 0x24, 0x06, 0x00, 0x01, // union
	7, 5, 0x81, 0x03, 8, 0, 16, // interrupt IN
	// interface 1: CDC data
	9, 4, 1, 0, 2, 0x0a, 0, 0, 0,
	7, 5, 0x02, 0x02, 64, 0, 0, // bulk OUT
	7, 5, 0x83, 0x02, 64, 0, 0, // bulk IN
}

var fakeStrings = map[uint8]string{1: "urdp", 2: "urdp fake ACM", 3: "0001"}

func fakeHello() *Hello {
	return &Hello{ActiveConfig: 1, Device: fakeDeviceDesc, Configs: [][]byte{fakeConfigDesc}, Name: "urdp fake ACM"}
}

func newFakeBrowser(t *testing.T, att Attacher) *fakeBrowser {
	f := &fakeBrowser{
		t:        t,
		out:      make(chan []byte, 1<<16),
		status:   make(chan []byte, 16),
		closed:   make(chan struct{}),
		coding:   []byte{0x80, 0x25, 0, 0, 0, 0, 8}, // 9600 8N1
		config:   1,
		inFlight: map[uint32]bool{},
	}
	var once sync.Once
	f.b = NewBridge(att, "1-1", f.recv, func() { once.Do(func() { close(f.closed) }) })
	f.b.Logf = t.Logf
	go func() {
		for c := range f.out {
			f.b.HandleMessage(c)
		}
	}()
	return f
}

// start opens the channel and sends Hello, as usb.js does.
func (f *fakeBrowser) start() {
	f.b.Open()
	f.deliver(fakeHello().Marshal())
}

func (f *fakeBrowser) waitStatus(want uint8) string {
	f.t.Helper()
	for {
		select {
		case m := <-f.status:
			if m[1] == want {
				return string(m[2:])
			}
			if m[1] == StatusError {
				f.t.Fatalf("bridge error: %s", m[2:])
			}
		case <-time.After(10 * time.Second):
			f.t.Fatalf("no status %d", want)
		}
	}
}

func (f *fakeBrowser) deliver(msg []byte) {
	f.dmu.Lock() // keep one message's chunks together
	defer f.dmu.Unlock()
	for _, c := range Frame(msg) {
		f.out <- c
	}
}

func (f *fakeBrowser) recv(chunk []byte) error {
	return f.framer.Feed(chunk, func(msg []byte) {
		if msg[0] == MsgStatus {
			f.status <- msg
			return
		}
		op, err := UnmarshalOp(msg)
		if err != nil {
			f.t.Errorf("bad op: %v", err)
			return
		}
		f.handle(op)
	})
}

func (f *fakeBrowser) opsOf(typ uint8) []*Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Op
	for _, o := range f.ops {
		if o.Type == typ {
			out = append(out, o)
		}
	}
	return out
}

func (f *fakeBrowser) done(d *Done) {
	f.mu.Lock()
	if !f.inFlight[d.ID] {
		f.mu.Unlock()
		f.t.Errorf("op %d completed twice", d.ID)
		return
	}
	delete(f.inFlight, d.ID)
	f.mu.Unlock()
	f.deliver(d.Marshal())
}

func (f *fakeBrowser) handle(op *Op) {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	if f.inFlight[op.ID] {
		f.t.Errorf("op id %d reused while in flight", op.ID)
	}
	f.inFlight[op.ID] = true
	f.mu.Unlock()
	switch op.Type {
	case MsgControl:
		f.done(f.control(op))
	case MsgSetConf:
		f.mu.Lock()
		f.config = op.Value
		f.mu.Unlock()
		f.done(&Done{ID: op.ID})
	case MsgSetAlt, MsgClear, MsgReset:
		f.done(&Done{ID: op.ID})
	case MsgTransfer:
		f.transfer(op)
	case MsgIso:
		// The descriptors have no iso endpoint, but for unit tests ep
		// 0x84 answers packet i with min(len, i+1) bytes of value i,
		// and ep 0x04 accepts everything. Others fail every packet.
		d := &Done{ID: op.ID}
		for i, l := range op.Iso {
			switch op.EP {
			case 0x84:
				n := min(l, uint32(i+1))
				d.Packets = append(d.Packets, PacketResult{Actual: n})
				d.Data = append(d.Data, bytes.Repeat([]byte{byte(i)}, int(n))...)
			case 0x04:
				d.Packets = append(d.Packets, PacketResult{Actual: l})
			default:
				d.Packets = append(d.Packets, PacketResult{Status: StatusEPROTO})
			}
		}
		f.done(d)
	}
}

func (f *fakeBrowser) transfer(op *Op) {
	f.mu.Lock()
	defer func() { f.mu.Unlock(); f.flush() }()
	switch op.EP {
	case 0x02:
		f.echo = append(f.echo, op.Data...)
		go f.done(&Done{ID: op.ID, Actual: uint32(len(op.Data))})
	case 0x83:
		f.pending = append(f.pending, op)
	case 0x81:
		f.parked = append(f.parked, op)
	default:
		go f.done(&Done{ID: op.ID, Status: StatusEPIPE})
	}
}

// flush completes pending bulk IN reads with echoed data.
func (f *fakeBrowser) flush() {
	f.mu.Lock()
	var ready []*Done
	for len(f.pending) > 0 && len(f.echo) > 0 {
		op := f.pending[0]
		f.pending = f.pending[1:]
		n := min(int(op.Length), len(f.echo), 64)
		ready = append(ready, &Done{ID: op.ID, Actual: uint32(n), Data: append([]byte(nil), f.echo[:n]...)})
		f.echo = f.echo[n:]
	}
	f.mu.Unlock()
	for _, d := range ready {
		f.done(d)
	}
}

func (f *fakeBrowser) control(op *Op) *Done {
	s := op.Setup
	reqType, req := s[0], s[1]
	value := binary.LittleEndian.Uint16(s[2:])
	length := int(binary.LittleEndian.Uint16(s[6:]))
	in := func(b []byte) *Done {
		b = b[:min(len(b), length)]
		return &Done{ID: op.ID, Actual: uint32(len(b)), Data: b}
	}
	stall := &Done{ID: op.ID, Status: StatusEPIPE}
	switch {
	case reqType == 0x80 && req == 6: // GET_DESCRIPTOR
		switch value >> 8 {
		case 1:
			return in(fakeDeviceDesc)
		case 2:
			return in(fakeConfigDesc)
		case 3:
			idx := uint8(value)
			if idx == 0 {
				return in([]byte{4, 3, 0x09, 0x04})
			}
			str, ok := fakeStrings[idx]
			if !ok {
				return stall
			}
			b := []byte{0, 3}
			for _, c := range utf16.Encode([]rune(str)) {
				b = binary.LittleEndian.AppendUint16(b, c)
			}
			b[0] = byte(len(b))
			return in(b)
		}
		return stall // qualifier, BOS, ...
	case reqType == 0x80 && req == 0: // GET_STATUS
		return in([]byte{0, 0})
	case reqType == 0x80 && req == 8: // GET_CONFIGURATION
		f.mu.Lock()
		defer f.mu.Unlock()
		return in([]byte{f.config})
	case reqType == 0x21 && req == 0x20: // SET_LINE_CODING
		f.mu.Lock()
		f.coding = append([]byte(nil), op.Data...)
		f.mu.Unlock()
		return &Done{ID: op.ID, Actual: uint32(len(op.Data))}
	case reqType == 0xa1 && req == 0x21: // GET_LINE_CODING
		f.mu.Lock()
		defer f.mu.Unlock()
		return in(append([]byte(nil), f.coding...))
	case reqType == 0x21 && req == 0x22: // SET_CONTROL_LINE_STATE
		return &Done{ID: op.ID}
	}
	return stall
}
