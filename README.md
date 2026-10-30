# urdp

A minimal, low-latency remote desktop that runs in the browser. It's a single Go binary that serves a web page and streams the desktop, or a single window, over WebRTC. Your mouse and keyboard are sent back the same way.

```
./urdp                 # serves http://<host>:8000
./urdp -addr :9000 -fps 60 -bitrate 20000
./urdp -list           # print capturable windows
URDP_PASSWORD=hunter2 ./urdp -hosts desk.example.com   # require a password; allow that hostname
```

- `http://host:8000/` streams the entire desktop.
- `http://host:8000/<window-id>` (e.g. `/0x1a00007`) streams one window, and input is mapped into that window.
- `http://host:8000/api/windows` lists windows as JSON. The source picker in the page uses it.
- Add `?probe` when viewing the same machine's desktop to measure capture→display latency in the browser (see below).

## How it works

| Stage | Linux (X11) | Windows |
|---|---|---|
| Desktop capture | XShm `XShmGetImage` on the root window | DXGI Desktop Duplication |
| Window capture | XComposite-redirected window + XShm | `PrintWindow(PW_RENDERFULLCONTENT)` |
| Change detection | XDamage | DXGI frame notifications |
| Video encode | libx264 (cgo) | libx264 (static, cgo) |
| Audio | PulseAudio/PipeWire monitor of the default sink → Opus | WASAPI loopback → Opus |
| Input | XTest | `SendInput` (scancodes) |

Every viewer of a source shares one capture→encode pipeline and one track. Audio is captured once and shared by all viewers.

### Latency choices

- **Event-driven capture.** Capture blocks on XDamage/DXGI until the screen actually changes, then grabs at once. Idle screens cost no encode or bandwidth. The FPS flag is only a cap.
- **x264 `ultrafast` + `zerolatency`.** No B-frames, no lookahead, sliced threads, one frame in and one frame out. The profile is constrained baseline, so browsers can always hardware-decode it. A VBV cap bounds burst sizes. IDR frames are sent on PLI/FIR and when a viewer joins, plus every `-keyint` seconds.
- **Fast BGRA→I420.** The conversion is vectorized and split across cores: about 0.6ms for a 1080p frame.
- **Congestion control.** Per viewer, Google Congestion Control (GCC) estimates bandwidth from transport-wide-cc feedback, and the x264 VBV cap is reconfigured on the fly. When several viewers share a source, the slowest one sets the cap. There is no send-side pacer, since one would delay every frame. GCC can't distinguish an idle screen from a slow link, because its estimate never exceeds 1.5× what was received. So the cap follows GCC only while the stream is actually using the bandwidth or GCC sees congestion. In a test with a 4 Mbit/s link and a busy terminal, this cut latency from about 210ms to about 70ms.
- **Zero playout delay.** The server stamps every video packet with the `playout-delay` RTP header extension (min=max=0), and the client sets `jitterBufferTarget = 0`. Chrome then renders each frame as soon as it's decoded.
- **Opus.** 10ms frames in `RESTRICTED_LOWDELAY` mode.
- **Input channels.** Mouse moves use an unordered, no-retransmit data channel: a lost position is simply replaced by the next one. Buttons and keys use a reliable channel and carry their own coordinates. Keys are sent as physical key codes (`KeyboardEvent.code`).
- **Local cursor.** The cursor is not captured. The browser's own pointer is the cursor, so it never lags.

### Measuring latency

Open `http://localhost:8000/?probe` on the machine being streamed, with the browser window visible on the captured screen. The page paints a binary timestamp in its corner and finds it again in the decoded video. The HUD then shows p50/p90 of how old each decoded frame is.

That number covers the sender page's paint, capture, encode, network, decode, and the receiving page's next frame callback. On a 1080p Xvfb display, the total is about 50ms p50. Of that, only about 8–10ms is server-side capture and encode. The rest is the browser's own paint and present pipeline, counted on both ends.

## Building

### Linux

```
sudo apt install pkg-config libx11-dev libxext-dev libxtst-dev libxcomposite-dev libxdamage-dev \
  libx264-dev libopus-dev libpulse-dev
make            # bin/urdp
make test
```

Run it inside an X11 session, with `DISPLAY` set. Wayland sessions are not supported yet. Under XWayland only X11 windows are visible.

### Windows

Cross-compile from Linux or WSL with mingw-w64. The result is a single static `urdp.exe` with no DLLs:

```
sudo apt install gcc-mingw-w64-x86-64 nasm
make windows    # builds static x264 + Opus into third_party/, then bin/urdp.exe
```

When running `urdp.exe` from WSL, Windows may ask whether to allow it through the firewall. Access from the same machine over loopback works either way. Allow it if you want other machines to connect.

### Networking notes

- The answer SDP includes all candidates, so no trickle-ICE signaling is needed. For LAN use no STUN/TURN is required. For NAT traversal, use `-stun stun:stun.l.google.com:19302`.
- The server also advertises loopback candidates. A browser on the same host can then connect even where host-IP UDP is firewalled, for example WSL2 mirrored networking.
- Behind 1:1 NAT (cloud VMs, containers), use `-nat1to1 <public-ip>`.

## Security

Anyone who can reach the port can see your screen and control your mouse and keyboard.

- Set `-password` (or `$URDP_PASSWORD`) to require HTTP basic auth. Browsers prompt for it, and any username works. Without TLS it is sent in the clear. For use beyond a trusted LAN, put the server behind an HTTPS reverse proxy, or bind to localhost (`-addr 127.0.0.1:8000`) and reach it over SSH or a VPN such as Tailscale.
- The API rejects cross-site requests and any `Host` header that isn't an IP, `localhost`, or a name passed in `-hosts`. This stops other websites from driving it through your browser (CSRF and DNS rebinding).
- A `/<window-id>` stream limits what is *shown*, not what can be *done*. Keyboard input and shortcuts still reach the whole desktop.

## Layout

```
main.go                  flags, embeds web/
internal/capture         Source interface; X11 + Windows backends; synthetic test source
internal/encoder         libx264 wrapper and BGRA→I420
internal/audio           loopback capture (PulseAudio / WASAPI) and Opus
internal/input           wire protocol, keymap, XTest / SendInput injectors
internal/server          HTTP + signaling, shared pipelines, playout-delay interceptor
web/                     browser client
```
