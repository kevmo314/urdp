# urdp Camera on Windows 11

The viewer's webcam can appear on a Windows 11 host as **urdp Camera**, a
Media Foundation virtual camera (`MFCreateVirtualCamera`). It's a user-mode
COM media source that the Windows Frame Server service loads. It is **not** a
kernel driver: there's no INF, `.sys`, service, or certificate.

urdp never installs it by itself. At run time the server only checks whether
the COM class is registered in HKLM. If it isn't, urdp uses an already
registered OBS Virtual Camera, and otherwise sends the viewer a notice on the
`input` data channel (`{"t":"notice","dev":"cam",...}`) that says to run
`urdp.exe -install-camera`.

## What `-install-camera` changes

It must be run from an elevated (Administrator) terminal. Otherwise it refuses
and changes nothing. It also refuses on builds older than 22000.

| What | Detail |
| --- | --- |
| Directory | `%ProgramData%\urdp\` (created if missing; never re-ACLed) and `%ProgramData%\urdp\camera\`. The camera directory is set to owner Administrators with a protected DACL: SYSTEM and Administrators get full control; Users, LOCAL SERVICE and ALL APPLICATION PACKAGES get read and execute. If either directory already exists, isn't owned by Administrators or SYSTEM, or is a junction or symlink, the install refuses. |
| File | `%ProgramData%\urdp\camera\urdpcam.dll`. This is the media source, built from `internal/vdev/mfcam/source.cpp` by `make windows` and embedded in `urdp.exe`. It's unsigned, and it imports only ADVAPI32, KERNEL32, MFPlat and msvcrt. |
| Registry | `HKLM\SOFTWARE\Classes\CLSID\{D8B04D25-43B5-4B57-9B1B-DA8E5F2D6C90}\InprocServer32`, with `(Default)` set to the DLL path and `ThreadingModel` set to `Both`. |

The camera device exists only while urdp holds a viewer camera session open:

- Opening a session calls `MFCreateVirtualCamera` with `MFVirtualCameraLifetime_Session` and `MFVirtualCameraAccess_CurrentUser`. This makes a software device node, visible only to the current user, that Windows removes when urdp calls `Remove()` or exits.
- Frames go through the named section `Global\urdp.Camera.v1`. The DLL creates it inside Frame Server, and only SYSTEM, LOCAL SERVICE, Administrators and INTERACTIVE users can open it.
- The mutex `Global\urdp.Camera.Producer.v1` allows one producer at a time.

## What `-uninstall-camera` changes

It must also be run elevated. It reverses the install:

1. It deletes the CLSID key and its `InprocServer32` subkey.
2. It deletes `urdpcam.dll`. If Frame Server still has the DLL loaded, uninstall renames it to `urdpcam.dll.<n>.old` and schedules that file for deletion at the next reboot (`MoveFileEx` with `DELAY_UNTIL_REBOOT`, which writes `PendingFileRenameOperations`).
3. It removes `%ProgramData%\urdp\camera` and `%ProgramData%\urdp`, but only if they are empty.

## INSTALL-TESTING

None of this has been run on a real host yet. Before you test, close every app
that uses a camera.

1. Build the binary on Linux or WSL with `make windows`, then copy `bin/urdp.exe` to the host.
2. Install from an **Administrator** PowerShell with `.\urdp.exe -install-camera`.
3. Check the install with these read-only commands:
   ```
   reg query "HKLM\SOFTWARE\Classes\CLSID\{D8B04D25-43B5-4B57-9B1B-DA8E5F2D6C90}\InprocServer32"
   icacls "$env:ProgramData\urdp\camera"
   ```
4. Run end to end:
   1. Start `urdp.exe` normally. It doesn't need to be elevated.
   2. Connect a viewer and press 📷.
   3. On the host, open the Camera app (or Teams, or a browser) and pick **urdp Camera**.
   4. While the session is open, `Get-PnpDevice -FriendlyName "urdp Camera"` should list the device. After you stop the viewer camera or quit urdp, it should be gone.
5. Optionally run the probe without a viewer. These test helpers create the camera from synthetic frames and validate the pixels:
   ```
   # on Linux/WSL
   make windows
   CGO_ENABLED=1 GOOS=windows CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
     PKG_CONFIG_LIBDIR=$PWD/third_party/windows/lib/pkgconfig \
     go test -c -o vdev.test.exe ./internal/vdev
   x86_64-w64-mingw32-g++ -O2 -static -o camera-probe.exe scripts/camera-probe-windows.cpp \
     -lmf -lmfplat -lmfreadwrite -lmfuuid -lole32 -luuid
   # on the host, two terminals
   $env:URDP_TEST_NATIVE_CAMERA=1; .\vdev.test.exe -test.run TestNativeCameraIntegration -test.v
   .\camera-probe.exe
   ```
6. Uninstall from an **Administrator** PowerShell with `.\urdp.exe -uninstall-camera`. After that, the `reg query` above should fail, and `%ProgramData%\urdp\camera` should be gone (or hold only a `*.old` file until you reboot).

If something goes wrong:

- **Frame Server holds an old DLL.** Reboot after you upgrade or uninstall.
- **Remove it by hand.** As Administrator, run `reg delete "HKLM\SOFTWARE\Classes\CLSID\{D8B04D25-43B5-4B57-9B1B-DA8E5F2D6C90}" /f`, then delete `%ProgramData%\urdp\camera`.
