// Command urdp is a minimal low-latency remote desktop server. It serves a
// web client and streams the desktop (or a single window) over WebRTC.
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
	"github.com/kevmo314/urdp/internal/server"
	"github.com/kevmo314/urdp/internal/usbip"
)

//go:embed web
var webFS embed.FS

func main() {
	var (
		addr      = flag.String("addr", ":8000", "HTTP listen address")
		fps       = flag.Int("fps", 60, "capture/encode frame rate")
		bitrate   = flag.Int("bitrate", 20000, "maximum video bitrate in kbps")
		keyint    = flag.Int("keyint", 30, "seconds between periodic keyframes (they're also sent whenever a viewer reports loss)")
		audioKbps = flag.Int("audio", 128, "Opus bitrate in kbps (0 disables audio)")
		stun      = flag.String("stun", "", "comma-separated STUN/TURN URLs, e.g. stun:stun.l.google.com:19302")
		nat1to1   = flag.String("nat1to1", "", "comma-separated public IPs to advertise as host candidates")
		hosts     = flag.String("hosts", "", "comma-separated hostnames clients may use to reach the server (IPs and localhost are always allowed)")
		codecs    = flag.String("codecs", "h264,h265,av1,vp9,vp8", "video codec preference; viewers can also ask for one with ?codec=")
		backend   = flag.String("encoder", "", `encoder backend: "" for automatic (hardware first), "hardware", "software", or a name such as "x264"`)
		mcpOn     = flag.Bool("mcp", true, "serve an MCP endpoint at /mcp so AI agents can take screenshots and drive input")
		list      = flag.Bool("list", false, "list capturable windows and exit")
		password  = flag.String("password", os.Getenv("URDP_PASSWORD"), "require this password (HTTP basic auth, any username); defaults to $URDP_PASSWORD")
		usb       = flag.Bool("usb", false, "experimental: let viewers attach their USB devices (WebUSB) to this host via USB/IP")
	)
	flag.Parse()

	if *list {
		wins, err := capture.List()
		if err != nil {
			log.Fatal(err)
		}
		for _, w := range wins {
			log.Printf("%-12s %5dx%-5d %s", capture.FormatID(w.ID), w.Width, w.Height, w.Title)
		}
		return
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	cfg := server.Config{
		WebFS:      web,
		Encoder:    encoder.Options{FPS: *fps, BitrateKbps: *bitrate, KeyintSeconds: *keyint, Backend: *backend},
		AudioKbps:  *audioKbps,
		DisableMCP: !*mcpOn,
	}
	if *stun != "" {
		cfg.ICEServers = []webrtc.ICEServer{{URLs: strings.Split(*stun, ",")}}
	}
	for _, name := range strings.Split(*codecs, ",") {
		c, err := encoder.ParseCodec(name)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Codecs = append(cfg.Codecs, c)
	}
	if *hosts != "" {
		cfg.AllowedHosts = strings.Split(strings.ToLower(*hosts), ",")
	}
	if *nat1to1 != "" {
		cfg.NAT1To1IPs = strings.Split(*nat1to1, ",")
	}
	if *usb {
		att, err := usbip.Default()
		if err != nil {
			log.Printf("usb: passthrough will fail: %v", err)
		} else {
			log.Printf("usb: passthrough enabled; viewers can plug USB devices into this machine")
		}
		cfg.USB = att
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("urdp listening on http://%s", ln.Addr())
	var h http.Handler = srv
	if *password != "" {
		h = basicAuth(h, *password)
	} else {
		log.Printf("warning: no -password set; anyone who can reach this port controls this machine")
	}
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Fatal(hs.Serve(ln))
}

func basicAuth(next http.Handler, password string) http.Handler {
	want := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, _ := r.BasicAuth()
		got := sha256.Sum256([]byte(pw))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="urdp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
