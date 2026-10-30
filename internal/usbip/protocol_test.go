package usbip

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// unhex decodes hex with whitespace and '|' separators, for golden bytes
// laid out like the offset tables in usbip_protocol.rst.
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	s = strings.NewReplacer(" ", "", "\n", "", "\t", "", "|", "").Replace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpHeaderGolden(t *testing.T) {
	// OP_REQ_DEVLIST: version 0x0111, code 0x8005, status 0.
	want := unhex(t, "0111 8005 00000000")
	if got := (OpHeader{Version, OpReqDevlist, 0}).Marshal(); !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
	h, err := ReadOpHeader(bytes.NewReader(want))
	if err != nil || h != (OpHeader{0x0111, 0x8005, 0}) {
		t.Fatalf("decoded %+v, %v", h, err)
	}
}

func TestImportRequestGolden(t *testing.T) {
	// OP_REQ_IMPORT: header then a 32-byte NUL-padded busid.
	want := unhex(t, "0111 8003 00000000 | 312d31"+strings.Repeat("00", 29))
	if got := MarshalImportRequest("1-1"); !bytes.Equal(got, want) {
		t.Fatalf("got %x\nwant %x", got, want)
	}
	r := bytes.NewReader(want)
	h, _ := ReadOpHeader(r)
	id, err := ReadBusID(r)
	if h.Code != OpReqImport || id != "1-1" || err != nil {
		t.Fatalf("got %v %q %v", h, id, err)
	}
}

var testInfo = DeviceInfo{
	Path:  "/sys/devices/pci0000:00/0000:00:1d.1/usb2/2-1",
	BusID: "2-1", BusNum: 2, DevNum: 3, Speed: SpeedHigh,
	IDVendor: 0x1209, IDProduct: 0x0001, BCDDevice: 0x0100,
	DeviceClass: 2, DeviceSubClass: 0, DeviceProtocol: 0,
	ConfigurationValue: 1, NumConfigurations: 1, NumInterfaces: 2,
	Interfaces: []Interface{{2, 2, 1}, {10, 0, 0}},
}

// deviceRecord is the 312-byte device record (offsets 0x0C..0x143 of
// OP_REP_DEVLIST, 0x08..0x13F of OP_REP_IMPORT).
func deviceRecord(t *testing.T) []byte {
	path := make([]byte, 256)
	copy(path, testInfo.Path)
	busid := make([]byte, 32)
	copy(busid, "2-1")
	rest := unhex(t, `
		00000002 | 00000003 | 00000003
		1209 | 0001 | 0100
		02 00 00 | 01 01 02`) // busnum devnum speed | vid pid bcdDevice | class sub proto | config nconfigs nifaces
	return append(append(path, busid...), rest...)
}

func TestDevlistReplyGolden(t *testing.T) {
	want := unhex(t, "0111 0005 00000000 | 00000001")
	want = append(want, deviceRecord(t)...)
	want = append(want, unhex(t, "02020100 0a000000")...) // per-interface class/sub/proto/pad
	got := MarshalDevlistReply([]DeviceInfo{testInfo})
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
	if len(got) != 8+4+312+8 {
		t.Fatalf("length %d", len(got))
	}
	r := bytes.NewReader(got)
	if h, _ := ReadOpHeader(r); h.Code != OpRepDevlist {
		t.Fatal(h)
	}
	devs, err := ReadDevlistReply(r)
	if err != nil || len(devs) != 1 || !reflect.DeepEqual(devs[0], testInfo) {
		t.Fatalf("decoded %+v, %v", devs, err)
	}
}

func TestImportReplyGolden(t *testing.T) {
	want := append(unhex(t, "0111 0003 00000000"), deviceRecord(t)...)
	got := MarshalImportReply(&testInfo)
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
	r := bytes.NewReader(got)
	h, _ := ReadOpHeader(r)
	d, err := ReadImportReply(r, h)
	info := testInfo
	info.Interfaces = nil // not part of OP_REP_IMPORT
	if err != nil || !reflect.DeepEqual(*d, info) {
		t.Fatalf("decoded %+v, %v", d, err)
	}
	if got := MarshalImportReply(nil); !bytes.Equal(got, unhex(t, "0111 0003 00000001")) {
		t.Fatalf("failure reply %x", got)
	}
}

func TestCmdSubmitGolden(t *testing.T) {
	// GET_DESCRIPTOR(device, 64) on ep0, as the hub driver sends it.
	wire := unhex(t, `
		00000001 0000000d 00010002 00000001 00000000
		00000200 00000040 00000000 00000000 00000000
		8006000100004000`)
	m, err := ReadMessage(bytes.NewReader(wire), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Header{
		Command: CmdSubmit, SeqNum: 13, DevID: 0x10002, Direction: DirIn, EP: 0,
		TransferFlags: 0x200, TransferBufferLength: 64,
		Setup: [8]byte{0x80, 6, 0, 1, 0, 0, 0x40, 0},
	}
	if m.Header != want || m.Data != nil || m.IsIso() {
		t.Fatalf("got %+v", m)
	}
	if got := m.Marshal(); !bytes.Equal(got, wire) {
		t.Fatalf("re-encoded %x", got)
	}
}

func TestCmdSubmitOutWithData(t *testing.T) {
	// Bulk OUT on ep 2 with 5 bytes, number_of_packets 0xffffffff as the
	// spec specifies for non-iso.
	wire := unhex(t, `
		00000001 00000020 00010002 00000000 00000002
		00000000 00000005 00000000 ffffffff 00000000
		0000000000000000
		68656c6c6f`)
	m, err := ReadMessage(bytes.NewReader(wire), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.EP != 2 || m.Direction != DirOut || string(m.Data) != "hello" || m.IsIso() {
		t.Fatalf("got %+v", m)
	}
	if !bytes.Equal(m.Marshal(), wire) {
		t.Fatal("re-encode mismatch")
	}
}

func TestIsoSubmitGolden(t *testing.T) {
	// Iso OUT on ep 1, two packets of 3 and 2 bytes, then the descriptors.
	wire := unhex(t, `
		00000001 00000005 00010002 00000000 00000001
		00000000 00000005 00000010 00000002 00000001
		0000000000000000
		0102030405
		00000000 00000003 00000000 00000000
		00000003 00000002 00000000 00000000`)
	m, err := ReadMessage(bytes.NewReader(wire), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []IsoPacket{{0, 3, 0, 0}, {3, 2, 0, 0}}
	if !m.IsIso() || m.StartFrame != 16 || m.Interval != 1 || !reflect.DeepEqual(m.Iso, want) || len(m.Data) != 5 {
		t.Fatalf("got %+v", m)
	}
	if !bytes.Equal(m.Marshal(), wire) {
		t.Fatal("re-encode mismatch")
	}
}

func TestRetSubmitGolden(t *testing.T) {
	// RET_SUBMIT: devid, direction and ep are 0; status -32 (stall) would
	// be ffffffe0. Here: success with 4 bytes of IN data.
	m := Message{Header: Header{Command: RetSubmit, SeqNum: 13, Status: 0, ActualLength: 4, NumberOfPackets: 0}, Data: []byte{1, 2, 3, 4}}
	want := unhex(t, `
		00000003 0000000d 00000000 00000000 00000000
		00000000 00000004 00000000 00000000 00000000
		0000000000000000
		01020304`)
	if got := m.Marshal(); !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
	in := func(uint32) uint32 { return DirIn }
	got, err := ReadMessage(bytes.NewReader(want), in)
	if err != nil || !bytes.Equal(got.Data, m.Data) || got.ActualLength != 4 {
		t.Fatalf("decoded %+v %v", got, err)
	}
	// For an OUT URB the same header carries no payload.
	out := func(uint32) uint32 { return DirOut }
	got, err = ReadMessage(bytes.NewReader(want[:48]), out)
	if err != nil || got.Data != nil {
		t.Fatalf("decoded %+v %v", got, err)
	}

	stall := Message{Header: Header{Command: RetSubmit, SeqNum: 1, Status: StatusEPIPE}}
	if b := stall.Marshal(); !bytes.Equal(b[20:24], []byte{0xff, 0xff, 0xff, 0xe0}) {
		t.Fatalf("stall status %x", b[20:24])
	}
}

func TestUnlinkGolden(t *testing.T) {
	cmd := unhex(t, `
		00000002 00000011 00010002 00000001 00000003
		0000000e 00000000 00000000 00000000 00000000
		0000000000000000`)
	m, err := ReadMessage(bytes.NewReader(cmd), nil)
	if err != nil || m.Command != CmdUnlink || m.UnlinkSeqNum != 14 || m.SeqNum != 17 {
		t.Fatalf("got %+v %v", m, err)
	}
	if !bytes.Equal(m.Marshal(), cmd) {
		t.Fatal("re-encode mismatch")
	}
	// RET_UNLINK with -ECONNRESET (-104 = 0xffffff98).
	ret := Message{Header: Header{Command: RetUnlink, SeqNum: 17, Status: StatusECONNRESET}}
	want := unhex(t, `
		00000004 00000011 00000000 00000000 00000000
		ffffff98 00000000 00000000 00000000 00000000
		0000000000000000`)
	if got := ret.Marshal(); !bytes.Equal(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestReadMessageRejects(t *testing.T) {
	bad := unhex(t, "00000009"+strings.Repeat("00", 44))
	if _, err := ReadMessage(bytes.NewReader(bad), nil); err == nil {
		t.Fatal("unknown command accepted")
	}
	huge := unhex(t, "00000001 00000001 00000000 00000000 00000001 00000000 7fffffff"+strings.Repeat("00", 20))
	if _, err := ReadMessage(bytes.NewReader(huge), nil); err != errTooLarge {
		t.Fatalf("huge transfer: %v", err)
	}
	trunc := unhex(t, "00000001 00000001")
	if _, err := ReadMessage(bytes.NewReader(trunc), nil); err == nil {
		t.Fatal("truncated header accepted")
	}
}
