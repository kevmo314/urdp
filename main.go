// Command urdp is a minimal low-latency remote desktop server. It serves a
// web client and streams the desktop (or a single window) over WebRTC.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
	"github.com/kevmo314/urdp/internal/server"
)

//go:embed web
var webFS embed.FS

func main() {
	var (
		addr      = flag.String("addr", ":8000", "HTTP listen address")
		fps       = flag.Int("fps", 60, "capture/encode frame rate")
		bitrate   = flag.Int("bitrate", 20000, "maximum video bitrate in kbps")
		keyint    = flag.Int("keyint", 5, "seconds between periodic keyframes")
		audioKbps = flag.Int("audio", 128, "Opus bitrate in kbps (0 disables audio)")
		stun      = flag.String("stun", "", "comma-separated STUN/TURN URLs, e.g. stun:stun.l.google.com:19302")
		nat1to1   = flag.String("nat1to1", "", "comma-separated public IPs to advertise as host candidates")
		list      = flag.Bool("list", false, "list capturable windows and exit")
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
		WebFS:     web,
		Encoder:   encoder.Options{FPS: *fps, BitrateKbps: *bitrate, KeyintSeconds: *keyint},
		AudioKbps: *audioKbps,
	}
	if *stun != "" {
		cfg.ICEServers = []webrtc.ICEServer{{URLs: strings.Split(*stun, ",")}}
	}
	if *nat1to1 != "" {
		cfg.NAT1To1IPs = strings.Split(*nat1to1, ",")
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
	log.Fatal(http.Serve(ln, srv))
}
