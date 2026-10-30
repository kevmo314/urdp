package usbip

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	h := fakeHello()
	b := h.Marshal()
	// type, version, active config, nconfigs, then LE u16 lengths.
	if !bytes.Equal(b[:6], []byte{MsgHello, WireVersion, 1, 1, 18, 0}) {
		t.Fatalf("header % x", b[:6])
	}
	got, err := UnmarshalHello(b)
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := UnmarshalHello(b[:len(b)-1]); err == nil {
		t.Fatal("truncated hello accepted")
	}
	bad := append([]byte(nil), b...)
	bad[1] = 99
	if _, err := UnmarshalHello(bad); err == nil {
		t.Fatal("wrong version accepted")
	}
}

func TestDoneGolden(t *testing.T) {
	d := &Done{ID: 7, Status: StatusEPIPE, Actual: 3, Packets: []PacketResult{{0, 2}, {StatusEPROTO, 0}}, Data: []byte{9, 9}}
	want := []byte{
		MsgDone, 7, 0, 0, 0, 0xe0, 0xff, 0xff, 0xff, 3, 0, 0, 0, 2, 0, 0, 0,
		0, 0, 0, 0, 2, 0, 0, 0, 0xb9, 0xff, 0xff, 0xff, 0, 0, 0, 0, 9, 9,
	}
	if got := d.Marshal(); !bytes.Equal(got, want) {
		t.Fatalf("got % x", got)
	}
	got, err := UnmarshalDone(want)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestOpRoundTrip(t *testing.T) {
	ops := []Op{
		{Type: MsgControl, ID: 1, Setup: [8]byte{0x80, 6, 0, 1, 0, 0, 18, 0}},
		{Type: MsgControl, ID: 2, Setup: [8]byte{0x21, 0x20, 0, 0, 0, 0, 7, 0}, Data: []byte{1, 2, 3, 4, 5, 6, 7}},
		{Type: MsgTransfer, ID: 3, EP: 0x83, Length: 512},
		{Type: MsgTransfer, ID: 4, EP: 0x02, Flags: TransferZLP, Data: []byte("hi")},
		{Type: MsgIso, ID: 5, EP: 0x84, Iso: []uint32{192, 192}},
		{Type: MsgSetConf, ID: 6, Value: 2},
		{Type: MsgSetAlt, ID: 7, Value: 1, Alt: 3},
		{Type: MsgClear, ID: 8, EP: 0x81},
		{Type: MsgReset, ID: 9},
	}
	for _, o := range ops {
		got, err := UnmarshalOp(o.Marshal())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Data) == 0 {
			got.Data = o.Data
		}
		if len(got.Iso) == 0 {
			got.Iso = o.Iso
		}
		if !reflect.DeepEqual(*got, o) {
			t.Errorf("got %+v want %+v", got, o)
		}
	}
	// Transfer layout: type, id LE, ep, flags, length LE.
	b := (&Op{Type: MsgTransfer, ID: 0x01020304, EP: 0x83, Length: 0x200}).Marshal()
	if !bytes.Equal(b, []byte{0x11, 4, 3, 2, 1, 0x83, 0, 0, 2, 0, 0}) {
		t.Fatalf("transfer % x", b)
	}
}

func TestFraming(t *testing.T) {
	msgs := [][]byte{{1}, bytes.Repeat([]byte{7}, 3*ChunkSize+5), {}, []byte("abc")}
	var stream []byte
	for _, m := range msgs {
		chunks := Frame(m)
		for _, c := range chunks {
			if len(c) > ChunkSize {
				t.Fatalf("chunk of %d bytes", len(c))
			}
			stream = append(stream, c...)
		}
	}
	// Reassemble from arbitrary splits.
	rng := rand.New(rand.NewSource(1))
	var f Framer
	var got [][]byte
	for len(stream) > 0 {
		n := min(len(stream), 1+rng.Intn(5000))
		if err := f.Feed(stream[:n], func(m []byte) { got = append(got, m) }); err != nil {
			t.Fatal(err)
		}
		stream = stream[n:]
	}
	if len(got) != len(msgs) {
		t.Fatalf("got %d messages", len(got))
	}
	for i := range msgs {
		if !bytes.Equal(got[i], msgs[i]) {
			t.Fatalf("message %d differs", i)
		}
	}
	if err := f.Feed([]byte{0xff, 0xff, 0xff, 0x7f}, func([]byte) {}); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestDevice(t *testing.T) {
	d, err := NewDevice(fakeHello(), "1-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.VendorID != fakeVID || d.ProductID != fakePID || d.Speed != SpeedFull {
		t.Fatalf("got %04x:%04x speed %d", d.VendorID, d.ProductID, d.Speed)
	}
	if d.maxPacket[0x02] != 64 || d.maxPacket[0x81] != 8 {
		t.Fatalf("maxPacket %v", d.maxPacket)
	}
	info := d.Info()
	if info.NumInterfaces != 2 || !reflect.DeepEqual(info.Interfaces, []Interface{{2, 2, 1}, {10, 0, 0}}) || info.DeviceClass != 2 {
		t.Fatalf("info %+v", info)
	}

	// 512-byte bulk endpoints: high speed.
	hs := fakeHello()
	hs.Configs = [][]byte{bytes.Clone(fakeConfigDesc)}
	hs.Configs[0][len(fakeConfigDesc)-3], hs.Configs[0][len(fakeConfigDesc)-2] = 0, 2 // bulk IN wMaxPacketSize 512
	if d, _ := NewDevice(hs, "1-1"); d.Speed != SpeedHigh {
		t.Fatalf("speed %d", d.Speed)
	}
	// USB 3 with a 512-byte ep0: SuperSpeed.
	ss := fakeHello()
	ss.Device = bytes.Clone(fakeDeviceDesc)
	ss.Device[3], ss.Device[7] = 3, 9
	if d, _ := NewDevice(ss, "1-1"); d.Speed != SpeedSuper {
		t.Fatalf("speed %d", d.Speed)
	}

	bad := fakeHello()
	bad.Device = bad.Device[:8]
	if _, err := NewDevice(bad, "1-1"); err == nil {
		t.Fatal("short device descriptor accepted")
	}
}
