# urdp

A minimal, low-latency remote desktop that runs in the browser. It's a single Go binary for Linux, Windows or macOS that serves a web page and streams the desktop, one monitor, or a single window over WebRTC, with system audio. Your mouse, keyboard and gamepads are sent back the same way. Your microphone, webcam and (experimentally) USB devices can show up on the host too.

Prebuilt binaries are on the [releases page](https://github.com/kevmo314/urdp/releases): `nightly` tracks `main`.

```
./urdp                 # serves http://<host>:8000
./urdp -addr :9000 -fps 60 -bitrate 20000
./urdp -list           # print capturable windows
URDP_PASSWORD=hunter2 ./urdp -hosts desk.example.com   # require a password; allow that hostname
```

- `http://host:8000/` lists every source as a `tree`-style listing: the desktop and its monitors, then windows grouped by application, with their icons. Click one to stream it.
- The desktop stream (the `desktop/` entry) covers all monitors stitched together. `/display1`, `/display2`, … stream one monitor each.
- `http://host:8000/<window-id>` (e.g. `/0x1a00007`) streams one window, and input is mapped into that window.
- The page uses these JSON APIs: `/api/displays`, `/api/windows` and `/api/windows/<id>/icon`.
- In the stream, the 🎤 and 📷 buttons share your microphone and camera with the host. Connected gamepads are forwarded automatically.
- Add `?probe` when viewing the same machine's desktop to measure capture→display latency in the browser (see below).

## How it works

| Stage | Linux (X11) | Windows | macOS |
|---|---|---|---|
| Desktop capture | XShm on the root window (cropped per monitor via XRandR) | DXGI Desktop Duplication per output, stitched, dirty rects only | ScreenCaptureKit per display, stitched |
| Window capture | XComposite-redirected window + XShm | Windows.Graphics.Capture (PrintWindow fallback) | ScreenCaptureKit window filter |
| Change detection | XDamage | DXGI / WGC frame events | SCFrameStatus |
| Video encode | libx264; libvpx (VP8/VP9), libaom (AV1); VA-API (H.264/HEVC) | libx264 (static) | libx264 (static) |
| Audio | PulseAudio/PipeWire monitor → Opus | WASAPI loopback → Opus | ScreenCaptureKit audio → Opus |
| Input | XTest | `SendInput` (scancodes) | CGEvent |
| Viewer mic | PulseAudio virtual source | VB-Audio Virtual Cable | — |
| Viewer camera | v4l2loopback | OBS Virtual Camera | — |
| Viewer gamepad | uinput (Xbox 360) | ViGEmBus (Xbox 360) | — |
| USB passthrough | vhci-hcd | usbip-win2 (untested) | — |

Every viewer of a source shares one capture→encode pipeline and one track. Audio is captured once and shared by all viewers.

### Latency choices

- **Event-driven capture.** Capture blocks on XDamage/DXGI until the screen actually changes, then grabs at once. Idle screens cost no encode or bandwidth. The FPS flag is only a cap.
- **x264 `ultrafast` + `zerolatency`.** No B-frames, no lookahead, sliced threads, one frame in and one frame out. The profile is constrained baseline, so browsers can always hardware-decode it. A VBV cap bounds burst sizes. IDR frames are sent on PLI/FIR and when a viewer joins, plus every `-keyint` seconds.
- **Other codecs (Linux).** `?codec=av1`, `vp9`, `vp8` or `h265` picks the viewer's codec. VP8/VP9 use libvpx and AV1 uses libaom, all in realtime mode: no lag frames, no frame dropping, CBR with a 100ms buffer, and keyframes only on request or every `-keyint` seconds, so each frame comes out of the encoder as it goes in. On a Ryzen 9 3900X, a 1080p frame where everything changes takes about 4ms with x264, 6ms with VP8, 7–8ms with VP9 and 10ms with AV1. SVT-AV1 2.3 in its low-delay mode took 21ms per frame, so libaom is used. A changing desktop is cheaper than that.
- **Fast BGRA→I420.** The conversion is vectorized and split across cores: about 0.6ms for a 1080p frame.
- **Congestion control.** Per viewer, Google Congestion Control (GCC) estimates bandwidth from transport-wide-cc feedback, and the x264 VBV cap is reconfigured on the fly. When several viewers share a source, the slowest one sets the cap. There is no send-side pacer, since one would delay every frame. GCC can't distinguish an idle screen from a slow link, because its estimate never exceeds 1.5× what was received. So the cap follows GCC only while the stream is actually using the bandwidth or GCC sees congestion. In a test with a 4 Mbit/s link and a busy terminal, this cut latency from about 210ms to about 70ms.
- **Zero playout delay.** The server stamps every video packet with the `playout-delay` RTP header extension (min=max=0), and the client sets `jitterBufferTarget = 0`. Chrome then renders each frame as soon as it's decoded.
- **Opus.** 10ms frames in `RESTRICTED_LOWDELAY` mode.
- **Input channels.** Mouse moves use an unordered, no-retransmit data channel: a lost position is simply replaced by the next one. Buttons and keys use a reliable channel and carry their own coordinates. Keys are sent as physical key codes (`KeyboardEvent.code`).
- **Local cursor.** The cursor is not captured. The browser's own pointer is the cursor, so it never lags.

### Measuring latency

Open `http://localhost:8000/?probe` on the machine being streamed, with the browser window visible on the captured screen. The page paints a binary timestamp in its corner and finds it again in the decoded video. The HUD then shows p50/p90 of how old each decoded frame is.

That number also includes the probe page's own rendering inside the browser on the host, which is usually a frame or two. To leave that out, run `scripts/xstamp.c` on the host, which draws the timestamp natively at the top-left of the screen, and open `/?probe=native`. On 1080p Xvfb at 60fps that measures **~21ms p50 / 24ms p90**: about 8ms for grab and encode, about 6ms waiting for the next 60fps capture slot (because xstamp changes the screen every millisecond), and about 8ms on the viewer for decode (~1ms) plus waiting for its next display refresh. Raising `-fps` above the viewer's refresh rate makes things worse, because Chrome then queues frames it can't present.

The default probe covers the sender page's paint, capture, encode, network, decode, and the receiving page's next frame callback. On a 1080p Xvfb display, the total is about 50ms p50. Of that, only about 8–10ms is server-side capture and encode. The rest is the browser's own paint and present pipeline, counted on both ends.

## Building

### Linux

```
sudo apt install pkg-config libx11-dev libxext-dev libxtst-dev libxcomposite-dev libxdamage-dev \
  libx264-dev libopus-dev libpulse-dev libvpx-dev libaom-dev libva-dev
make            # bin/urdp
make test
```

Run it inside an X11 session, with `DISPLAY` set. Wayland sessions are not supported yet. Under XWayland only X11 windows are visible.

`make linux-release` links x264, Opus, libvpx and libaom statically. libva is loaded at run time, if it's present.

#### Hardware encoding (VA-API)

H.264 and HEVC can be encoded on the GPU through VA-API (`vaapi-h264`, `vaapi-hevc`). Hardware backends are tried first and fall back to software if the device, driver or profile is missing. Use `-encoder software` to skip them, or `-encoder vaapi-h264` to require one. The low-power (fixed-function) entrypoint is used when the driver has one. Frames are IDR and P only, with CBR and a 100ms HRD buffer (or VBR or CQP when that's all the driver offers). urdp writes the parameter sets and slice headers itself, as FFmpeg does.

- urdp needs read/write access to `/dev/dri/renderD128`, which the `render` group normally gives (`sudo usermod -aG render $USER`), and a VA driver such as `intel-media-va-driver` or `mesa-va-drivers`. `vainfo` lists what the driver can encode. Set `URDP_VAAPI_DEVICE` to use a different render node.
- **Not yet tested on real hardware.** The packed headers are checked against FFmpeg's parser, but no encode has been run on a GPU. Under WSL2 with kernel 6.18, Mesa's d3d12 VA driver can't initialize, because libdrm doesn't recognize the new `faux` bus that `/dev/dri` sits on.

#### Viewer devices

The viewer's microphone, webcam and gamepads can appear on the host as virtual devices. Each device needs some host setup:

- **Microphone.** Needs PulseAudio or PipeWire's pulse server. While a viewer shares their mic, urdp loads a null sink and a remapped source. Apps then list it as an input called "urdp Microphone". The modules are unloaded when the last viewer stops.
- **Camera.** Needs v4l2loopback: `sudo apt install v4l2loopback-dkms`, then `sudo modprobe v4l2loopback exclusive_caps=1 card_label="urdp Camera"`. To load it at boot, put `v4l2loopback` in `/etc/modules-load.d/` and the options in `/etc/modprobe.d/`. urdp picks the device labelled "urdp Camera", or else any loopback device. Your user needs write access to it, which the `video` group normally gives.
- **Gamepad.** Each pad becomes a virtual Xbox 360 controller through `/dev/uinput`, which is root-only by default. To allow access, add a udev rule and join the `input` group, then log in again:

  ```
  echo 'KERNEL=="uinput", MODE="0660", GROUP="input"' | sudo tee /etc/udev/rules.d/60-urdp-uinput.rules
  sudo udevadm control --reload && sudo udevadm trigger /dev/uinput
  sudo usermod -aG input $USER
  ```

### Windows

Cross-compile from Linux or WSL with mingw-w64. The result is a single static `urdp.exe` with no DLLs:

```
sudo apt install gcc-mingw-w64-x86-64 nasm
make windows    # builds static x264 + Opus into third_party/, then bin/urdp.exe
```

Viewer devices on Windows need drivers that can't be bundled:

- **Microphone.** Install [VB-Audio Virtual Cable](https://vb-audio.com/Cable/). urdp plays into "CABLE Input", and apps pick "CABLE Output" as their microphone. Set `URDP_MIC_DEVICE` to use a different render endpoint.
- **Camera.** Install OBS Studio and register its virtual camera once as Administrator, using `data\obs-plugins\win-dshow\virtualcam-install.bat` in the OBS folder. urdp feeds frames into "OBS Virtual Camera", and OBS doesn't need to be running.
- **Gamepad.** Install [ViGEmBus](https://github.com/nefarius/ViGEmBus/releases). Each pad appears as an Xbox 360 controller.

When running `urdp.exe` from WSL, Windows may ask whether to allow it through the firewall. Access from the same machine over loopback works either way. Allow it if you want other machines to connect.

### macOS

`make mac` builds static x264 and Opus for macOS 13 and later, then `bin/urdp`. The binary links only system frameworks. The first time it runs, grant permissions in **System Settings → Privacy & Security** to the app that launches it (Terminal, or `sshd-keygen-wrapper` when started over SSH):

- **Screen & System Audio Recording**, for capture and audio.
- **Accessibility**, for mouse and keyboard input.

Then restart urdp. Viewer devices aren't implemented on macOS yet.

### Networking notes

- The answer SDP includes all candidates, so no trickle-ICE signaling is needed. For LAN use no STUN/TURN is required. For NAT traversal, use `-stun stun:stun.l.google.com:19302`.
- The server also advertises loopback candidates. A browser on the same host can then connect even where host-IP UDP is firewalled, for example WSL2 mirrored networking.
- Behind 1:1 NAT (cloud VMs, containers), use `-nat1to1 <public-ip>`.

## Security

Anyone who can reach the port can see your screen and control your mouse and keyboard.

- Set `-password` (or `$URDP_PASSWORD`) to require HTTP basic auth. Browsers prompt for it, and any username works. Without TLS it is sent in the clear. For use beyond a trusted LAN, put the server behind an HTTPS reverse proxy, or bind to localhost (`-addr 127.0.0.1:8000`) and reach it over SSH or a VPN such as Tailscale.
- The API rejects cross-site requests and any `Host` header that isn't an IP, `localhost`, or a name passed in `-hosts`. This stops other websites from driving it through your browser (CSRF and DNS rebinding).
- A `/<window-id>` stream limits what is *shown*, not what can be *done*. Keyboard input and shortcuts still reach the whole desktop.

## USB passthrough (experimental)

With `-usb`, the viewer can plug a USB device from their own computer into the host. Click **USB** in the HUD, then pick a device. The host then sees it as a real USB device, so its normal drivers bind and `lsusb` lists it. The flag is off by default, because it lets anyone who can view the host attach arbitrary USB devices to it.

- **Browser:** WebUSB opens the device and claims its interfaces. Each device gets its own reliable `usb` data channel. The server translates every URB into one WebUSB call (`controlTransferIn/Out`, `transferIn/Out`, `isochronousTransferIn/Out`, `selectConfiguration`, `selectAlternateInterface`, `clearHalt` or `reset`). See `internal/usbip/wire.go` for the protocol.
- **Linux host:** the server speaks USB/IP to the kernel's `vhci-hcd` directly. It gives the kernel one end of a socket pair through `/sys/devices/platform/vhci_hcd.0/attach`, which is what `usbip attach` does. Run `sudo modprobe vhci-hcd` first. Writing that file needs root, or group write access (`chgrp`/`chmod g+w` on `attach` and `detach`).
- **Windows host:** install [usbip-win2](https://github.com/vadimgrn/usbip-win2) and put `usbip.exe` on PATH. The server runs a one-device USB/IP server on 127.0.0.1 and runs `usbip.exe attach` against it. This path is untested on Windows.
- The device is detached when the viewer unplugs it, the channel closes or the viewer disconnects.

Limits:

- WebUSB needs Chrome or Edge on a secure origin (HTTPS or localhost).
- The browser refuses to claim the protected interface classes, which are HID, mass storage, audio, video, audio/video, smart card and wireless controller, and it blocklists security keys. So in practice this works for vendor-specific and CDC devices.
- The viewer's OS must not have a driver bound to the device. Desktop Linux needs udev read/write access and no kernel driver on the interface. Windows needs the WinUSB driver.
- Isochronous transfers are forwarded, but Chrome doesn't support them on Windows.
- WebUSB can't cancel a transfer. The host's cancelled reads are answered at once. Data that a cancelled read later receives goes to the next read on that endpoint.

## Layout

```
main.go                  flags, embeds web/
internal/capture         Source interface; X11 + Windows backends; synthetic test source
internal/encoder         encoder backends (x264, libvpx, libaom, VA-API) and BGRA→YUV
internal/audio           loopback capture (PulseAudio / WASAPI) and Opus
internal/input           wire protocol, keymap, XTest / SendInput injectors
internal/server          HTTP + signaling, shared pipelines, playout-delay interceptor
internal/usbip           USB passthrough: USB/IP codec, vhci-hcd / usbip-win2 attach, WebUSB bridge
web/                     browser client
```
