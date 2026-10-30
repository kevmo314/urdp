# urdp

A minimal, low-latency remote desktop that runs in the browser. It's a single Go binary that serves a web page and streams the desktop, or a single window, over WebRTC. Your mouse and keyboard are sent back the same way.

```
./urdp                 # serves http://<host>:8000
./urdp -addr :9000 -fps 60 -bitrate 20000
./urdp -list           # print capturable windows
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

**There is no authentication.** Anyone who can reach the port can see your screen and control your mouse and keyboard. Bind to localhost (`-addr 127.0.0.1:8000`) and tunnel over SSH or a VPN such as Tailscale, or put an authenticating proxy in front.

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
