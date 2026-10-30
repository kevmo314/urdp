# Bundled Windows drivers

`urdp.exe -install-drivers` supports Windows x64. The EXE embeds 251,754 bytes
of compressed, unmodified driver packages (see `internal/windrivers/packages.zip`;
its actual size is authoritative). There are no downloaded installation-time
assets. One ShellExecuteEx `runas` operation elevates the same EXE, with a
five-minute parent wait and output relay. An already elevated invocation needs
no prompt. Driver setup does not start the server.

Packages are extracted under the system Program Files known folder at
`urdp\drivers`, which ordinary users cannot modify. SetupCopyOEMInf records
published OEM INF names, `pnputil /add-driver /install` installs the packages,
and missing root devices are registered through SetupDiCreateDeviceInfo,
SetupDiCallClassInstaller(DIF_REGISTERDEVICE), and
UpdateDriverForPlugAndPlayDevices. There is no bundled devcon helper.

A protected ownership journal records only newly staged packages and newly
created devices. Repeat installation retains existing healthy devices, including
ViGEmBus installations owned by other applications. Uninstall removes only
recorded instances and newly staged OEM packages, without force deletion; a
Windows-required reboot is reported, never performed automatically. A failed
operation preserves the remaining journal so it can be retried. Existing devices
that are not started produce an explicit repair error rather than duplicate root
devices. Existing USB/IP installations must implement the 0.9.8.1 ABI used here;
urdp does not force a downgrade or upgrade of somebody else's controller.

## Packages and signatures

| Device | Release/package | License | Verified SYS and CAT signer |
| --- | --- | --- | --- |
| Gamepad | [ViGEmBus v1.22.0 installer](https://github.com/nefarius/ViGEmBus/releases/tag/v1.22.0), driver 1.21.442.0 | BSD-3-Clause in the released package (not MIT) | Microsoft Windows Hardware Compatibility Publisher; `Valid` |
| USB/IP | [usbip-win2 v.0.9.8.1](https://github.com/vadimgrn/usbip-win2/releases/tag/v.0.9.8.1), UDE and filter 12.1.30.403 | BSD-2-Clause for this version (not GPL-3.0) | Microsoft Windows Hardware Compatibility Publisher; `Valid` |

The full license texts and source links are in [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES),
also embedded in the EXE and installed alongside the drivers. USB/IP uses the
upstream public IOCTL ABI directly; no upstream user-mode program, library, or
Microsoft C++ runtime is redistributed. The kernel connects to urdp's loopback
USB/IP server, imports the browser device once, and delivers URBs through the
existing bridge. Attach is cancellable and close explicitly detaches the port.

[Machine-readable signature evidence](windows-driver-signatures.json) records
Get-AuthenticodeSignature status, signer subject, and SHA-256 for every embedded
file. [The manifest](../internal/windrivers/packages.sha256.json) pins those bytes.
Re-run `scripts/windows/verify-driver-signatures.ps1` on Windows to extract the
ZIP, check every hash, and require a valid Microsoft WHCP signature on every SYS
and CAT. INF files are catalog-signed, not individually Authenticode-signed;
Windows validates package membership when staging/installing the INF. Successful
kernel loading on the test host additionally exercises Windows code integrity.

Independent check from Linux (osslsigncode 2.13, chain checked against
"Microsoft Root Certificate Authority 2010" read from the Windows root store):
all three CATs and all three SYS files verify to Microsoft Windows Hardware
Compatibility Publisher, each SYS is a member of its CAT, and each INF's SHA-1
and SHA-256 appear in its CAT. The usbip-win2 SYS files carry two embedded
signatures: the first is the upstream author's self-signed `WDKTestCert vadim`,
the second (nested) is Microsoft's. The kernel only needs the Microsoft one.
Both LICENSE files are byte-identical to the upstream tags, and the two release
installers below match their published hashes. The SYS bytes themselves were
not re-extracted from those installers in this check.

Release archive SHA-256:

```
89220a7865076b342892f98865f3499fb7c4cfd673159e89d352c360fd014c6a  ViGEmBus_1.22.0_x64_x86_arm64.exe
38cad6d4432b52d5bb9409d9ad03b72fdffc4ada4cd3a48fbeca1a2752a8518a  USBip-0.9.8.1-x64.exe
```

For package updates, download those official assets and verify the archive
hashes. ViGEm's Advanced Installer bootstrapper supports `/extract` and
`/extractlzma`; it may extract into its fixed ProgramData cache instead of the
requested directory. Use only its x64 INF/SYS/CAT and LICENSE, not x86/ARM64 files
or nefcon. usbip-win2's Inno installer can be unpacked using InnoUnpacker 2;
its `{tmp}` contains the two driver packages. Preserve all signed files byte for
byte. ZIP them under `vigem/` and `usbip-0.9.8.1/`, including their licenses,
then update the manifest, signature report, and notices. Do not rebuild the SYS,
rewrite the INF, or generate a replacement CAT.

## Install testing

Nothing in the server installs drivers. `urdp.exe` with no driver flags only
looks for the devices when a viewer uses them: a missing ViGEmBus is reported
to the viewer as a `{t:"notice", dev:"gamepad"}` message on the `input`
channel, and a missing USB/IP controller is reported on that device's `usb`
channel status. Both messages say to run `urdp.exe -install-drivers`.

Run these yourself, from an ordinary (non-elevated) prompt on the Windows x64
host. Each shows one UAC prompt and prints what it did.

```
urdp.exe -install-drivers      # install
urdp.exe -uninstall-drivers    # undo exactly what the install recorded
```

What `-install-drivers` changes:

- Writes `C:\Program Files\urdp\drivers\` (the two package folders,
  `THIRD_PARTY_NOTICES`, and `installed.json`, which records what urdp added).
- ViGEmBus, only if no `Nefarius\ViGEmBus\Gen1` device exists already: stages
  `ViGEmBus.inf` into the driver store as `oemNN.inf`, creates one root device,
  and adds the kernel service `ViGEmBus` (boot-time start, `StartType=1`). An
  existing working ViGEmBus is left alone; a broken one stops the install with
  a request to repair it in Device Manager.
- USB/IP, only if no `ROOT\USBIP_WIN2\UDE` device exists: stages
  `usbip2_filter.inf` and `usbip2_ude.inf` as `oemNN.inf`, creates one root
  device `ROOT\USB\NNNN` (the emulated USB host controller), and adds kernel
  services `usbip2_ude` and `usbip2_filter` (demand start).
  **`usbip2_filter.inf` is an extension INF that adds an upper filter driver to
  every physical `USB\ROOT_HUB30` hub on the machine** (`ErrorControl=2`), and
  `pnputil /add-driver /install` applies it right away. This is how upstream
  usbip-win2 installs too, but it puts third-party kernel code under all of your
  USB 3 ports (keyboard and mouse included). Windows may ask for a reboot to
  finish attaching it.

What `-uninstall-drivers` does: removes the root devices listed in
`installed.json`, runs `pnputil /delete-driver oemNN.inf /uninstall` for each
package urdp staged (never `/force`), then deletes `C:\Program Files\urdp\drivers`.
Devices and packages that existed before urdp are kept. A reboot that Windows
asks for is reported, never performed.

Manual check after install (read-only):

```
pnputil /enum-devices /class System | findstr /i vigem
pnputil /enum-drivers | findstr /i "usbip vigem"
sc qc usbip2_ude & sc qc usbip2_filter & sc qc ViGEmBus
type "C:\Program Files\urdp\drivers\installed.json"
```

Functional tests that use the installed drivers (they create a virtual pad and
attach a simulated USB device):

```
set URDP_TEST_WINDOWS_DRIVERS=1
go test ./internal/vdev -run TestWindowsGamepadXInput -v
go test ./internal/usbip -run TestWindowsNativeAttach -v
```

Then start `urdp.exe -usb`, connect a viewer, and plug in a gamepad and a
WebUSB device.

If `-uninstall-drivers` can't run, the manual cleanup is the upstream one:
`pnputil /remove-device` for the `ROOT\USB\NNNN` device in `installed.json`,
then `pnputil /delete-driver oemNN.inf /uninstall` for each listed package.

## InputInjector experiment

[The standalone MinGW C++ probe](../scripts/windows/inputinjector-probe.cpp)
defines the needed WinRT ABI interfaces without SDK projection headers:

```
x86_64-w64-mingw32-g++ -static scripts/windows/inputinjector-probe.cpp -lruntimeobject -o inputinjector-probe.exe
```

It uses RoInitialize and RoGetActivationFactory on
`Windows.UI.Input.Preview.Injection.InputInjector`. `TryCreate` belongs to
IInputInjectorStatics; IInputInjectorStatics2 instead exposes
TryCreateForAppBroadcastOnly. IInputInjector2 exposes the gamepad methods.
The probe sets A and left-stick X=0.5 in InjectedInputGamepadInfo and repeatedly
calls InjectGamepadInput for 30 seconds. Any command-line argument selects
observation only (WGI count and all four XInput slots).

On the real unpackaged Windows 11 Pro 10.0.26300 host, without elevation:

- RoInitialize, both activation factories, TryCreate, QueryInterface for
  IInputInjector2, InitializeGamepadInjection, gamepad-info activation, and
  InjectGamepadInput all returned `0x00000000`; TryCreate returned non-null.
- WGI Gamepad.Gamepads.Size remained zero after repeated injections.
- All four XInputGetState slots returned 1167 (device not connected).
- Windows Chrome 155.0.8059.41 `navigator.getGamepads()` remained eight null
  entries during concurrent sampling while the injector was running.

The unpackaged app was **not refused** by TryCreate on this host. Successful
injection still did not create an observable game controller. This result does
not establish game compatibility and does not justify making it the default.
The [Microsoft API documentation](https://learn.microsoft.com/en-us/uwp/api/windows.ui.input.preview.injection.inputinjector.injectgamepadinput)
also specifies the restricted inputInjectionBrokered package capability. urdp
therefore retains ViGEmBus as its working Xbox 360 backend.

## Microphone limitation

No suitable released, Microsoft-attested/WHQL-signed, redistributable virtual
microphone package was found in this investigation. This is a finding about the
candidates checked, not proof that no such package can ever exist. A normal
user-mode process cannot register a WASAPI render/capture endpoint pair;
implementing another audio kernel driver would still require Microsoft signing.

- [VirtualDrivers/Virtual-Audio-Driver 25.7.14](https://github.com/VirtualDrivers/Virtual-Audio-Driver/releases/tag/25.7.14)
  is MIT. Its SYS and CAT both report Authenticode `Valid`, but their signer is
  **SignPath Foundation**, not Microsoft WHCP. Ordinary Authenticode validity
  does not establish Secure Boot kernel acceptance. The release ZIP hash is
  `dd10560994de65a7e587fb8b93c0d7e9838292d9c3566a0976c2786d727292bd`.
- [Scream 4.0](https://github.com/duncanthrax/scream/releases/tag/4.0)
  SYS/CAT signatures report `UnknownError`, signer Tom Kistner, not Microsoft.
  It is also a network sound output driver rather than the required cable pair.
- [RadioVoice](https://github.com/doctorspider42/radio-voice#virtual-audio-driver)
  documents test signing and the need for future attestation. It does not
  satisfy the requirement.

None of these drivers, nor VB-Cable, is bundled. Existing VB-Cable WASAPI support
is preserved: urdp renders into **CABLE Input** and applications select
**CABLE Output** as their microphone. `URDP_MIC_DEVICE` still selects an exact
alternative render endpoint. Camera code and its dependencies are outside this
change.

## Verification

Host: Windows 11 Pro build 26300, Secure Boot `True` from elevated
Confirm-SecureBootUEFI. Distinct test executables were copied under
`C:\Users\Public\urdp-dev`. The running `C:\Users\Public\urdp\urdp.exe` was
never stopped.

- `make vet-windows windows` passed.
- Linux `go vet ./... && go test ./internal/...` passed.
- Real installer self-elevation completed with one UAC prompt and relayed logs.
  It retained the existing healthy ViGEmBus and created a USB/IP root controller.
  Windows requested a reboot for the filter; none was performed.
- Repeat install, owned-driver uninstall, and reinstall passed in one elevated
  lifecycle test. The original ViGEmBus instance stayed intact; USB/IP was left
  installed. The ownership journal never claimed the pre-existing gamepad bus.
- Opt-in TestWindowsGamepadXInput passed: XInput received A and LX=16384.
  Independent observation saw one WGI gamepad and Chrome identified
  `Xbox 360 Controller (XInput STANDARD GAMEPAD)`, with A=1 and left X about 0.5.
- Opt-in TestWindowsNativeAttach passed: the signed driver attached the simulated
  browser device, and Windows requested its descriptors through the bridge.
  This exercises native IOCTL layout, loopback import, URBs, and detach. It is
  not a test of every physical WebUSB device or game.

The host integration tests run only with `URDP_TEST_WINDOWS_DRIVERS=1`; ordinary
unit tests do not create kernel devices. `URDP_TEST_GAMEPAD_HOLD=1` holds the
controller for 30 seconds for independent WGI/browser observation.
