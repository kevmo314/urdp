# urdp for Apple TV

A native tvOS viewer for urdp. It negotiates with the server the same way the web page does: one non-trickle offer to `/api/offer`, then the `input`, `move` and `gamepad` data channels. Video is decoded by WebRTC (VideoToolbox for H.264/H.265) and shown through `AVSampleBufferDisplayLayer`, with every frame marked for immediate display.

## Build

```
brew install xcodegen
cd tvos && xcodegen generate && open urdp.xcodeproj
```

WebRTC comes from [livekit/webrtc-xcframework](https://github.com/livekit/webrtc-xcframework) through Swift Package Manager. Its tvOS simulator slice is arm64 only, so simulator builds need an Apple silicon Mac. `scripts/icons.py` regenerates the icon and top shelf images.

## Controls

| Input | Action |
|---|---|
| Siri Remote swipe | move the pointer (with acceleration) |
| Click | left button; hold it to drag |
| Play/Pause | right click |
| Hold Play/Pause and swipe | scroll |
| Back | menu: type text, send keys (Esc, Alt+Tab, Ctrl+Alt+Del, …), stats, disconnect |
| Game controllers | forwarded to the host as gamepads (up to 4) |
| Hold Menu + Options for 1s on a controller | open the menu |
| Bluetooth keyboard / mouse | forwarded as is (keys by physical position) |

The host's cursor isn't part of the video, so the app draws it, using the shapes the host sends.

Debug builds accept `-urdpServer http://host:8000 [-urdpSource display1] [-urdpPassword …] [-urdpStats YES]` as launch arguments to connect straight away.

## Release

`scripts/release.sh` archives with the Okaeri Communications distribution certificate (imported into a throwaway keychain) and the "urdp tvOS App Store" profile, then exports an `.ipa`, or uploads it to TestFlight with `UPLOAD=1`. It needs an App Store Connect API key and the certificate; see the header of the script. New builds go to the "Internal" TestFlight group automatically.
