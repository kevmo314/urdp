package usbip

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

func TestServeImport(t *testing.T) {
	info := testInfo
	// OP_REQ_DEVLIST: one device, then the connection closes.
	c, s := net.Pipe()
	go func() { _, _ = c.Write(OpHeader{Version, OpReqDevlist, 0}.Marshal()) }()
	done := make(chan bool)
	go func() { ok, _ := ServeImport(s, info); done <- ok }()
	h, err := ReadOpHeader(c)
	if err != nil || h.Code != OpRepDevlist {
		t.Fatalf("%+v %v", h, err)
	}
	devs, err := ReadDevlistReply(c)
	if err != nil || len(devs) != 1 || devs[0].BusID != "2-1" {
		t.Fatalf("%+v %v", devs, err)
	}
	if <-done {
		t.Fatal("devlist reported as import")
	}

	// Unknown bus id: status 1.
	c, s = net.Pipe()
	go func() { _, _ = c.Write(MarshalImportRequest("9-9")) }()
	go func() { ok, _ := ServeImport(s, info); done <- ok }()
	h, _ = ReadOpHeader(c)
	if _, err := ReadImportReply(c, h); err == nil {
		t.Fatal("import of unknown busid succeeded")
	}
	if <-done {
		t.Fatal("bad import accepted")
	}

	// Import: the connection stays open for the URB phase.
	c, s = net.Pipe()
	go func() { _, _ = c.Write(MarshalImportRequest("2-1")) }()
	go func() { ok, _ := ServeImport(s, info); done <- ok }()
	h, _ = ReadOpHeader(c)
	d, err := ReadImportReply(c, h)
	if err != nil || d.IDVendor != 0x1209 {
		t.Fatalf("%+v %v", d, err)
	}
	if !<-done {
		t.Fatal("import not accepted")
	}
	go func() { _, _ = s.Write([]byte("urb")) }()
	buf := make([]byte, 3)
	if _, err := c.Read(buf); err != nil || string(buf) != "urb" {
		t.Fatal("connection not usable after import")
	}

	// Wrong protocol version.
	c, s = net.Pipe()
	go func() { _, _ = c.Write(OpHeader{0x0106, OpReqImport, 0}.Marshal()) }()
	if ok, err := ServeImport(s, info); ok || err == nil {
		t.Fatal("old version accepted")
	}
}

// TestHelperUsbipClient is not a test: TestCommandAttach runs the test
// binary as a stand-in for `usbip attach`. It lists devices, imports the
// one it was told to, sends a CMD_SUBMIT and exits, like usbip-win2's CLI
// after its driver has connected.
func TestHelperUsbipClient(t *testing.T) {
	if os.Getenv("URDP_FAKE_USBIP_CLIENT") == "" {
		t.Skip("helper process")
	}
	args := os.Args[len(os.Args)-2:]
	addr, busID := "127.0.0.1:"+args[0], args[1]
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(OpHeader{Version, OpReqDevlist, 0}.Marshal())
	ReadOpHeader(c)
	devs, err := ReadDevlistReply(c)
	if err != nil || len(devs) != 1 || devs[0].BusID != busID {
		t.Fatalf("devlist %+v %v", devs, err)
	}
	c.Close()

	c, err = net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(MarshalImportRequest(busID))
	h, _ := ReadOpHeader(c)
	if _, err := ReadImportReply(c, h); err != nil {
		t.Fatal(err)
	}
	m := Message{Header: Header{Command: CmdSubmit, SeqNum: 1, Direction: DirIn, TransferBufferLength: 18,
		Setup: [8]byte{0x80, 6, 0, 1, 0, 0, 18, 0}}}
	c.Write(m.Marshal())
	fmt.Println("successfully attached to port 5")
}

func TestCommandAttach(t *testing.T) {
	t.Setenv("URDP_FAKE_USBIP_CLIENT", "1")
	dev, err := NewDevice(fakeHello(), "1-7")
	if err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/detached"
	c := Command{
		AttachArgs: []string{os.Args[0], "-test.run=^TestHelperUsbipClient$", "--", "{port}", "{busid}"},
		DetachArgs: []string{"sh", "-c", "echo {vport} > " + marker},
	}
	att, err := c.Attach(dev)
	if err != nil {
		t.Fatal(err)
	}
	if att.String() != "usbip port 5" {
		t.Fatalf("attachment %q", att)
	}
	m, err := ReadMessage(att, nil)
	if err != nil || m.Command != CmdSubmit || m.Setup[1] != 6 {
		t.Fatalf("%+v %v", m, err)
	}
	if err := att.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(marker); strings.TrimSpace(string(b)) != "5" {
		t.Fatalf("detach command saw %q", b)
	}

	// A failing client reports its output.
	bad := Command{AttachArgs: []string{"sh", "-c", "echo no vhci driver; exit 3"}}
	if _, err := bad.Attach(dev); err == nil || !strings.Contains(err.Error(), "no vhci driver") {
		t.Fatalf("got %v", err)
	}
}
