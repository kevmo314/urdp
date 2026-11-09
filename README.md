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

## AI agents (MCP)

urdp serves an [MCP](https://modelcontextprotocol.io) endpoint at `/mcp`, so an agent can see and drive the host. Turn it off with `-mcp=false`.

```
claude mcp add --transport http urdp http://host:8000/mcp
# with -password set:
claude mcp add --transport http urdp http://host:8000/mcp --header "Authorization: Basic $(printf x:PASSWORD | base64)"
```

| Tool | What it does |
|---|---|
| `list_sources` | monitors and windows (ids, titles, apps, sizes) |
| `screenshot` | the desktop, one monitor (`display1`) or a window (`0x…`); PNG or JPEG, downscaled to 1280px wide by default |
| `click`, `move`, `drag`, `scroll` | pointer actions at `(x, y)` in the most recent screenshot's pixels for that source |
| `type_text` | types ASCII text (US layout) |
| `key` | a key or combination: `Enter`, `ctrl+c`, `alt+F4`, `cmd+space` |
| `focus_window` | raises and focuses a window |

## How it works

| Stage | Linux (X11) | Windows | macOS |
|---|---|---|---|
| Desktop capture | XShm on the root window (cropped per monitor via XRandR) | DXGI Desktop Duplication per output, stitched, dirty rects only | ScreenCaptureKit per display, stitched |
| Window capture | XComposite-redirected window + XShm | Windows.Graphics.Capture (PrintWindow fallback) | ScreenCaptureKit window filter |
| Change detection | XDamage | DXGI / WGC frame events | SCFrameStatus |
| Video encode | libx264; libvpx (VP8/VP9), libaom (AV1); VA-API (H.264/HEVC) | AMD AMF (H.264/HEVC/AV1) from GPU textures via a minimal static FFmpeg; libx264 fallback (static) | VideoToolbox H.264/HEVC; libx264 fallback (static) |
| Audio | PulseAudio/PipeWire monitor → Opus | WASAPI loopback → Opus | ScreenCaptureKit audio → Opus |
| Cursor shape | XFixes events | GetCursorInfo at 60 Hz | NSCursor.currentSystemCursor at 60 Hz |
| Input | XTest | `SendInput` (scancodes) | CGEvent |
| Viewer mic | PulseAudio virtual source | VB-Audio Virtual Cable | built-in CoreAudio driver |
| Viewer camera | PipeWire camera node (v4l2loopback fallback) | urdp Camera (Windows 11, opt-in install; OBS Virtual Camera fallback) | — |
| Viewer gamepad | uinput (Xbox 360) | ViGEmBus (Xbox 360) | — |
| USB passthrough | vhci-hcd | bundled usbip-win2 | — |

Every viewer of a source shares one capture→encode pipeline and one track. Audio is captured once and shared by all viewers.

### Latency choices

- **Event-driven capture.** Capture blocks on XDamage/DXGI until the screen actually changes, then grabs at once. Idle screens cost no encode or bandwidth. The FPS flag is only a cap.
- **x264 `ultrafast` + `zerolatency`.** No B-frames, no lookahead, sliced threads, one frame in and one frame out. The profile is constrained baseline, so browsers can always hardware-decode it. A VBV cap bounds burst sizes. IDR frames are sent on PLI/FIR and when a viewer joins, plus every `-keyint` seconds.
- **Other codecs (Linux).** `?codec=av1`, `vp9`, `vp8` or `h265` picks the viewer's codec. VP8/VP9 use libvpx and AV1 uses libaom, all in realtime mode: no lag frames, no frame dropping, CBR with a 100ms buffer, and keyframes only on request or every `-keyint` seconds, so each frame comes out of the encoder as it goes in. On a Ryzen 9 3900X, a 1080p frame where everything changes takes about 4ms with x264, 6ms with VP8, 7–8ms with VP9 and 10ms with AV1. SVT-AV1 2.3 in its low-delay mode took 21ms per frame, so libaom is used. A changing desktop is cheaper than that.
- **Fast BGRA→I420.** The conversion is vectorized and split across cores: about 0.6ms for a 1080p frame.
- **Congestion control.** Per viewer, Google Congestion Control (GCC) estimates bandwidth from transport-wide-cc feedback, and the x264 VBV cap is reconfigured on the fly. When several viewers share a source, the slowest one sets the cap. There is no send-side pacer, since one would delay every frame. GCC can't distinguish an idle screen from a slow link, because its estimate never exceeds 1.5× what was received. So the cap follows GCC only while the stream is actually using the bandwidth or GCC sees congestion. In a test with a 4 Mbit/s link and a busy terminal, this cut latency from about 210ms to about 70ms.
- **FlexFEC.** For VP8, VP9 and AV1, each frame is followed by FlexFEC-03 repair packets. These are interleaved XOR parity, groups of at most 48 packets, about 10% overhead rising with the viewer's reported loss. Groups end at frame boundaries, so a lost packet is rebuilt as soon as its frame's repair arrives, without a round trip. Chrome ignores FlexFEC for H.264 and H.265, so it isn't sent for those. With retransmissions disabled and 3% loss, it let Chrome decode 3.3x (VP9) and 1.4x (AV1) as many frames. With retransmissions on, at 40–80ms round trips, it made no measurable latency difference in testing.
- **Zero playout delay.** The server stamps every video packet with the `playout-delay` RTP header extension (min=max=0), and the client sets `jitterBufferTarget = 0`. Chrome then renders each frame as soon as it's decoded.
- **Playback buffer (optional).** Over a jittery network (e.g. a long intercontinental path), frames arrive in clumps, and rendering each one immediately makes video stutter. The viewer's buffer picker (Live, 50–750ms, or `?smooth=<ms>`) leaves the playout-delay extension out of the offer and sets `jitterBufferTarget` on both tracks instead, so playback is even and audio stays roughly in step, at that much extra latency. Sizes change in place; switching to or from Live reconnects. `/api/stats` shows each viewer's frame-interval spread to judge it.
- **Opus.** 10ms frames in `RESTRICTED_LOWDELAY` mode.
- **Input channels.** Mouse moves use an unordered, no-retransmit data channel: a lost position is simply replaced by the next one. Buttons and keys use a reliable channel and carry their own coordinates. Keys are sent as physical key codes (`KeyboardEvent.code`).
- **Local cursor.** The cursor is not captured. The browser's own pointer is the cursor, so it never lags. Host shape changes travel over the reliable input channel as CSS names or PNG images with hotspots; each viewer caches shapes by ID. PNGs retain host-pixel sizing, capped at 128 pixels. Windows reports cursor visibility; macOS visibility is best effort. X11 recognizes transparent cursor shapes as hidden, but XFixes does not expose visibility changes made with `XFixesHideCursor`.

### Measuring latency

Open `http://localhost:8000/?probe` on the machine being streamed, with the browser window visible on the captured screen. The page paints a binary timestamp in its corner and finds it again in the decoded video. The HUD then shows p50/p90 of how old each decoded frame is.

That number also includes the probe page's own rendering inside the browser on the host, which is usually a frame or two. To leave that out, run `scripts/xstamp.c` on the host, which draws the timestamp natively at the top-left of the screen, and open `/?probe=native`. On 1080p Xvfb at 60fps that measures **~21ms p50 / 24ms p90**: about 8ms for grab and encode, about 6ms waiting for the next 60fps capture slot (because xstamp changes the screen every millisecond), and about 8ms on the viewer for decode (~1ms) plus waiting for its next display refresh. Raising `-fps` above the viewer's refresh rate makes things worse, because Chrome then queues frames it can't present.

The default probe covers the sender page's paint, capture, encode, network, decode, and the receiving page's next frame callback. On a 1080p Xvfb display, the total is about 50ms p50. Of that, only about 8–10ms is server-side capture and encode. The rest is the browser's own paint and present pipeline, counted on both ends.

## Building

### Linux

```
sudo apt install pkg-config libx11-dev libxfixes-dev libxext-dev libxtst-dev libxcomposite-dev libxdamage-dev \
  libx264-dev libopus-dev libpulse-dev libpipewire-0.3-dev libvpx-dev libaom-dev libva-dev
make            # bin/urdp
make test
```

Run it inside an X11 session, with `DISPLAY` set. Wayland sessions are not supported yet. Under XWayland only X11 windows are visible.

`make linux-release` links x264, Opus, libvpx and libaom statically. libva is loaded at run time, if it's present.

#### Hardware encoding (VA-API)

H.264 and HEVC can be encoded on the GPU through VA-API (`vaapi-h264`, `vaapi-hevc`). **These backends are experimental: they haven't run on a real GPU yet, so they're only used when you pass `-encoder vaapi-h264` (or `vaapi-hevc`).** If the device, driver or profile is missing, the server falls back to software. The low-power (fixed-function) entrypoint is used when the driver has one. Frames are IDR and P only, with CBR and a 100ms HRD buffer (or VBR or CQP when that's all the driver offers). urdp writes the parameter sets and slice headers itself, as FFmpeg does.

- urdp needs read/write access to `/dev/dri/renderD128`, which the `render` group normally gives (`sudo usermod -aG render $USER`), and a VA driver such as `intel-media-va-driver` or `mesa-va-drivers`. `vainfo` lists what the driver can encode. Set `URDP_VAAPI_DEVICE` to use a different render node.
- **Not yet tested on real hardware.** The packed headers are checked against FFmpeg's parser, but no encode has been run on a GPU. Under WSL2 with kernel 6.18, Mesa's d3d12 VA driver can't initialize, because libdrm doesn't recognize the new `faux` bus that `/dev/dri` sits on.

#### Viewer devices

The viewer's microphone, webcam and gamepads can appear on the host as virtual devices. Each device needs some host setup:

- **Microphone.** Needs PulseAudio or PipeWire's pulse server. While a viewer shares their mic, urdp loads a null sink and a remapped source. Apps then list it as an input called "urdp Microphone". The modules are unloaded when the last viewer stops.
- **Camera.** If PipeWire is running (the default on current Ubuntu, Fedora and most desktops), urdp publishes a PipeWire camera node, "urdp Camera" (`node.name` `urdp-camera`, `media.class` `Video/Source`, `media.role` `Camera`). No kernel module or root is needed. It offers I420, NV12 and YUY2 at the viewer's frame size and renegotiates when that size changes. Apps that use PipeWire cameras see it: Chrome and Firefox (through the camera portal; in Chrome, enable `chrome://flags/#enable-webrtc-pipewire-camera`), OBS's "Video Capture Device (PipeWire)" source, GStreamer's `pipewiresrc`, and Snapshot/Cheese. You can check it with `pw-cli ls Node`.

  Apps that only speak V4L2 can't see PipeWire cameras. That includes older Zoom and Skype builds, ffmpeg `-f v4l2`, and many Electron apps. For those, install v4l2loopback (an out-of-tree kernel module) instead: `sudo apt install v4l2loopback-dkms`, then `sudo modprobe v4l2loopback exclusive_caps=1 card_label="urdp Camera"`. To load it at boot, put `v4l2loopback` in `/etc/modules-load.d/` and the options in `/etc/modprobe.d/`. urdp uses it when PipeWire isn't reachable, or always with `URDP_CAMERA=v4l2` (`URDP_CAMERA=pipewire` forces PipeWire). It picks the device labelled "urdp Camera", or else any loopback device. Your user needs write access to it, which the `video` group normally gives.
- **Gamepad.** Each pad becomes a virtual Xbox 360 controller through `/dev/uinput`, which is root-only by default. To allow access, add a udev rule and join the `input` group, then log in again:

  ```
  echo 'KERNEL=="uinput", MODE="0660", GROUP="input"' | sudo tee /etc/udev/rules.d/60-urdp-uinput.rules
  sudo udevadm control --reload && sudo udevadm trigger /dev/uinput
  sudo usermod -aG input $USER
  ```

### Windows

Cross-compile from Linux or WSL with mingw-w64. The result is a single `urdp.exe` with statically linked codecs and an embedded camera DLL:

```
sudo apt install gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 nasm pkg-config
make windows    # builds codecs and camera source, then bin/urdp.exe
```

#### Hardware encoding (Windows)

`make windows` builds a minimal static FFmpeg (only libavcodec/libavutil with the AMF, NVENC and Media Foundation encoders and D3D11 frames; about 10 MB) and links it in. The vendor runtimes are loaded only when an encoder opens, so `urdp.exe` still starts on machines without them.

- **Zero-copy.** Desktop Duplication frames stay on the GPU: monitors on the same adapter share one D3D11 device, dirty rectangles are copied texture-to-texture (also when stitching monitors), and the encoder gets the BGRA texture with one GPU copy into FFmpeg's frame pool; AMF converts it to BT.709 YUV itself. Rotated monitors and the GDI fallback still produce CPU frames, which are uploaded. On an RX 7900 XTX this halves encode time (3440×1440 H.264: 3.6ms vs 7.7ms from CPU memory; 4880×2560 HEVC: 7.6ms vs 18.4ms) and skips the CPU readback of changed frames.
- **No added latency.** Each encoder is forced into its low-latency mode (no B-frames, no lookahead, one frame in flight, forced IDR on request) and must return each frame's packet before the next frame is submitted; one that holds frames back is refused. One hardware session runs at a time.
- **AMD AMF** (`libav-h264_amf`, `libav-hevc_amf`, `libav-av1_amf`) is tested and used automatically. H.264 tops out at 4096 pixels wide, so a wider desktop falls back to x264 (use H.265). RDNA3's AV1 encoder pads widths to a multiple of 64, which WebRTC can't crop, so it is refused at those sizes.
- **NVENC and Media Foundation** (`libav-h264_nvenc`, `libav-hevc_nvenc`, `libav-av1_nvenc`, `libav-h264_mf`, `libav-hevc_mf`) haven't been tested on real hardware yet, so they're only used when named with `-encoder`.

`go test -tags libav ./internal/encoder -run TestLibavD3D11` with `URDP_TEST_ENCODER=h264_amf` (and `WSLENV=URDP_TEST_ENCODER` when running from WSL) encodes a texture through the GPU path and the CPU path; `URDP_TEST_DUMP` saves the streams for checking colors with a decoder. `-run TestLibavD3D11Moving` encodes content that changes on every row and every frame (`URDP_TEST_SIZE=3440x1440` to pick a size); check the dumps with `scripts/check-moving.py <dump> h264 <WxH>`. FFmpeg's default D3D11 texture array is avoided: AMF reads BGRA array slices with 32-row blocks swapped.

On Windows x64, run `urdp.exe -install-drivers` once. It requests elevation once,
installs bundled Microsoft-signed gamepad/USB-IP drivers, and prints whether a
reboot is needed. Existing working devices are retained. To remove devices and
packages installed by urdp, run `urdp.exe -uninstall-drivers`; pre-existing
installations are preserved. No separate driver installer, USB/IP CLI, or VC++
runtime is required. The compressed driver bundle adds about 246 KiB.

urdp never installs drivers unless you run these commands. Without them, the server tells the viewer which device is missing and what to run.
See [install testing](docs/windows-drivers.md#install-testing) for exactly what install and uninstall change. Note that USB/IP adds an upper filter driver to every USB 3 root hub.

[Driver versions, licenses, signature evidence, and host test results](docs/windows-drivers.md).
Viewer devices on Windows:

- **Microphone.** Install [VB-Audio Virtual Cable](https://vb-audio.com/Cable/). urdp plays into "CABLE Input", and apps pick "CABLE Output" as their microphone. Set `URDP_MIC_DEVICE` to use a different render endpoint.
- **Camera (Windows 11, build 22000+).** Nothing is installed unless you ask. Run `urdp.exe -install-camera` once from an Administrator terminal to copy the embedded media source to `%ProgramData%\urdp\camera\urdpcam.dll` and register its COM class in HKLM for the Windows Frame Server; `urdp.exe -uninstall-camera` removes both. It is not a driver. Then enable the viewer's 📷 button and pick **urdp Camera** in the host app. The camera device only exists while that viewer camera session is open. Without the install, urdp falls back to an already registered OBS Virtual Camera, and otherwise tells the viewer what to run. See [docs/windows-camera.md](docs/windows-camera.md) for exactly what changes and how to test it.

  The native source accepts incoming I420 frames up to 4096×2160, converts them to NV12 in a shared ring, and scales it, keeping its aspect ratio with black bars, to the app's selected 1280×720, 640×480, or 1920×1080 format at 30 fps. If the viewer pauses, apps keep receiving the last frame (black before the first frame).
- **Gamepad.** The bundled ViGEmBus driver exposes each pad as an Xbox 360 controller. Run `urdp.exe -install-drivers` first, or install [ViGEmBus](https://github.com/nefarius/ViGEmBus/releases) yourself.

When running `urdp.exe` from WSL, Windows may ask whether to allow it through the firewall. Access from the same machine over loopback works either way. Allow it if you want other machines to connect.

### macOS

`make mac` builds static x264 and Opus for macOS 13 and later, then `bin/urdp`. The binary links only system frameworks. The first time it runs, grant permissions in **System Settings → Privacy & Security** to the app that launches it (Terminal, or `sshd-keygen-wrapper` when started over SSH):

- **Screen & System Audio Recording**, for capture and audio.
- **Accessibility**, for mouse and keyboard input.

Then restart urdp.

#### Viewer devices

- **Microphone.** urdp carries its own CoreAudio driver (an AudioServerPlugIn, ad-hoc signed, built by `make mac`). Install it once with `sudo ./urdp -install-audio-driver`. This copies `urdp-audio.driver` into `/Library/Audio/Plug-Ins/HAL` and restarts `coreaudiod`, which interrupts audio for a moment. Apps then list an input called "urdp Microphone" (48kHz stereo). The device is a loopback: urdp plays the viewer's microphone into its output side, and apps record its input side. Because of that, it also shows up in the output list, but it never becomes the default output. To remove it, run `sudo rm -rf /Library/Audio/Plug-Ins/HAL/urdp-audio.driver && sudo killall coreaudiod`. Apps that record from it need the usual Microphone permission.
- **Camera and gamepad.** These aren't possible without Apple-issued signing. A virtual camera must be a CoreMediaIO camera extension, and macOS only activates a system extension from an app signed with a Developer ID and the system extension entitlement. The older DAL plug-ins no longer load. A virtual gamepad needs `IOHIDUserDevice`, which requires the restricted `com.apple.developer.hid.virtual.device` entitlement. Without it, creation fails even as root, and an ad-hoc signed binary that claims the entitlement is killed at launch.

VideoToolbox hardware H.264 (`vt-h264`) and HEVC (`vt-hevc`) are registered only when the hardware encoder probe succeeds. Both run in real time with frame reordering off, no frame delay, and speed prioritized over quality, and each `Encode` waits for that frame's own output before returning Annex B (about 7 ms for 1080p H.264 and 5 ms for HEVC on an M1). Bitrate changes apply on the next frame: they set the average, with a hard limit of 1.5x over any second. Apple's low-latency rate control mode isn't used. On an M1 it ignored the rate limit for keyframes and then dropped most frames to catch up. M1 has no AV1 hardware encoder.

For a single display or window, the pipeline switches ScreenCaptureKit to IOSurface-backed NV12 (BT.709 matrix, sRGB color space) and hands its retained CVPixelBuffer directly to VideoToolbox without a CPU readback or color conversion. Stitched desktops use BGRA and the shared NV12 converter; software fallback keeps BGRA capture.

`go test -v ./internal/encoder -run TestVideoToolbox` checks 1080p CPU and synthetic IOSurface input, the session's low-latency properties, keyframes exactly when forced or at the interval, parameter sets, one output per input, and live bitrate changes. Install `ffmpeg` to also verify that each decoded frame matches its input and that the stream signals BT.709 with no B-frames. `go test -v ./internal/server -run TestVideoToolboxWebRTC` checks H.264 and H.265 negotiation and packet flow with a Mac-local Pion client; set `URDP_TEST_SERVER_URL=http://127.0.0.1:8000` to test a separately running server using its synthetic `test` source. Live capture tests require `URDP_TEST_SCREEN=1` and Screen Recording permission.

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
- **Windows x64 host:** run `urdp.exe -install-drivers` once. The server starts a one-device USB/IP server on 127.0.0.1 and asks the bundled usbip-win2 driver to attach through its native IOCTL ABI. It uses low-latency receive mode and explicitly detaches the assigned virtual port when the viewer disconnects. No `usbip.exe`, DLLs, PATH changes, or VC++ runtime installation is needed. Kernel attach, descriptor requests, and detach have been tested on a Secure Boot Windows host using a simulated browser USB device; arbitrary physical WebUSB devices remain experimental.
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
