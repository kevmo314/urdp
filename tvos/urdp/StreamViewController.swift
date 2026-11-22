import UIKit
import GameController
import LiveKitWebRTC

/// Full-screen stream of one source. As a GCEventViewController with
/// controller interaction off, controller and remote input goes to the
/// GameController handlers below rather than driving UIKit focus.
///
/// Siri Remote: swipe moves the pointer, click is a left click (hold it to
/// drag), Play/Pause is a right click, and Play/Pause held while swiping
/// scrolls. Back opens the stream menu. Gamepads are forwarded to the host
/// as gamepads; holding Menu and Options together for a second opens the
/// menu. Bluetooth keyboards and mice are forwarded as they are.
final class StreamViewController: GCEventViewController {
    private let api: API
    private let source: Source
    private var session: Session?
    var onClose: (() -> Void)?

    private let videoView = VideoView()
    private var videoTrack: LKRTCVideoTrack?
    private var frameSize = CGSize.zero
    private let cursorView = UIImageView()
    private let statusLabel = UILabel()
    private let statsLabel = UILabel()
    private var statsTimer: Timer?
    private var lastStats: Session.Stats?
    private var closed = false
    private var reconnecting = false

    // Pointer, normalized to the source.
    private var pointer = CGPoint(x: 0.5, y: 0.5)
    private var pendingMove: [String: Any]?
    private var cursorHotspot = CGPoint.zero
    private var cursorHidden = false
    private var cursorCSS: [UInt64: String] = [:]
    private var cursors: [UInt64: (UIImage, CGPoint)] = [:]

    // Siri Remote.
    private var lastTouch: CGPoint?
    private var scrolling = false
    private var scrolled = false
    private var leftHeld = false

    // Gamepads, by the index the host sees.
    private var padIndex: [ObjectIdentifier: Int] = [:]
    private var padSent: [Int: (String, CFTimeInterval)] = [:]
    private var menuChordSince: CFTimeInterval?

    // Keyboard auto-repeat (GameController reports only down and up).
    private var repeatKey: String?
    private var repeatAt: CFTimeInterval = 0

    private var displayLink: CADisplayLink?
    private var observers: [NSObjectProtocol] = []
    private var menuOpen = false

    init(api: API, source: Source) {
        self.api = api
        self.source = source
        super.init(nibName: nil, bundle: nil)
        modalPresentationStyle = .fullScreen
    }

    required init?(coder: NSCoder) { fatalError() }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .black
        controllerUserInteractionEnabled = false

        videoView.frame = view.bounds
        videoView.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        videoView.onSizeChange = { [weak self] size in
            self?.frameSize = size
            self?.placeCursor()
        }
        view.addSubview(videoView)

        cursorView.isHidden = true
        cursorView.layer.shadowColor = UIColor.black.cgColor
        cursorView.layer.shadowOpacity = 0.6
        cursorView.layer.shadowRadius = 2
        cursorView.layer.shadowOffset = .zero
        view.addSubview(cursorView)
        setCursor(css: "default")

        statusLabel.font = .systemFont(ofSize: 34, weight: .medium)
        statusLabel.textColor = .white
        statusLabel.textAlignment = .center
        statusLabel.numberOfLines = 0
        statusLabel.backgroundColor = UIColor.black.withAlphaComponent(0.6)
        statusLabel.layer.cornerRadius = 16
        statusLabel.clipsToBounds = true
        view.addSubview(statusLabel)

        statsLabel.font = .monospacedSystemFont(ofSize: 22, weight: .regular)
        statsLabel.textColor = .white
        statsLabel.numberOfLines = 0
        statsLabel.backgroundColor = UIColor.black.withAlphaComponent(0.55)
        statsLabel.isHidden = true
        view.addSubview(statsLabel)

        toast("Connecting to \(source.title)…")

        let nc = NotificationCenter.default
        observers.append(nc.addObserver(forName: .GCControllerDidConnect, object: nil, queue: .main) { [weak self] n in
            if let c = n.object as? GCController { self?.attach(c) }
        })
        observers.append(nc.addObserver(forName: .GCControllerDidDisconnect, object: nil, queue: .main) { [weak self] n in
            if let c = n.object as? GCController { self?.detach(c) }
        })
        observers.append(nc.addObserver(forName: .GCKeyboardDidConnect, object: nil, queue: .main) { [weak self] n in
            if let k = n.object as? GCKeyboard { self?.attach(k) }
        })
        observers.append(nc.addObserver(forName: .GCMouseDidConnect, object: nil, queue: .main) { [weak self] n in
            if let m = n.object as? GCMouse { self?.attach(m) }
        })
        GCController.controllers().forEach(attach)
        if let k = GCKeyboard.coalesced { attach(k) }
        GCMouse.mice().forEach(attach)

        let link = CADisplayLink(target: self, selector: #selector(tick))
        link.add(to: .main, forMode: .common)
        displayLink = link

        connect()
        #if DEBUG
        if UserDefaults.standard.bool(forKey: "urdpStats") { toggleStats() }
        #endif
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        layoutStatus()
        let stats = statsLabel.sizeThatFits(CGSize(width: view.bounds.width, height: .greatestFiniteMagnitude))
        statsLabel.frame = CGRect(origin: CGPoint(x: 60, y: 40), size: stats)
        placeCursor()
    }

    private func layoutStatus() {
        let size = statusLabel.sizeThatFits(CGSize(width: view.bounds.width - 200, height: .greatestFiniteMagnitude))
        let w = size.width + 60, h = size.height + 30
        statusLabel.frame = CGRect(x: (view.bounds.width - w) / 2, y: view.bounds.height - h - 80, width: w, height: h)
    }

    private func toast(_ text: String?) {
        statusLabel.text = text
        statusLabel.isHidden = text == nil
        layoutStatus()
    }

    // MARK: connection

    private func connect() {
        let s = Session(api: api, source: source.id)
        s.delegate = self
        session = s
        Task { @MainActor in
            do {
                try await s.connect()
            } catch is SourceGone {
                close(message: "\(source.title) was closed")
            } catch {
                guard s === session, !closed else { return }
                toast("Error: \(error.localizedDescription)\nRetrying…")
                s.close()
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
                    guard let self, s === self.session, !self.closed else { return }
                    self.connect()
                }
            }
        }
    }

    private func reconnect() {
        guard !closed, !reconnecting else { return }
        reconnecting = true
        session?.close()
        videoTrack?.remove(videoView)
        videoTrack = nil
        connect()
        reconnecting = false
    }

    func close(message: String? = nil) {
        guard !closed else { return }
        closed = true
        releaseAll()
        session?.close()
        session = nil
        videoTrack?.remove(videoView)
        displayLink?.invalidate()
        statsTimer?.invalidate()
        observers.forEach(NotificationCenter.default.removeObserver)
        GCController.controllers().forEach { detachHandlers($0) }
        if let k = GCKeyboard.coalesced { k.keyboardInput?.keyChangedHandler = nil }
        GCMouse.mice().forEach { m in
            m.mouseInput?.mouseMovedHandler = nil
            m.mouseInput?.leftButton.pressedChangedHandler = nil
            m.mouseInput?.rightButton?.pressedChangedHandler = nil
            m.mouseInput?.middleButton?.pressedChangedHandler = nil
            m.mouseInput?.scroll.valueChangedHandler = nil
        }
        dismiss(animated: true) { [onClose] in
            onClose?()
            if let message { NotificationCenter.default.post(name: .urdpNotice, object: message) }
        }
    }

    // MARK: pointer

    /// The rectangle the video occupies, letterboxed into the view.
    private var contentRect: CGRect {
        let b = view.bounds
        var size = frameSize
        if size.width <= 0 || size.height <= 0 {
            size = source.width > 0 && source.height > 0 ? CGSize(width: source.width, height: source.height) : b.size
        }
        let scale = min(b.width / size.width, b.height / size.height)
        let w = size.width * scale, h = size.height * scale
        return CGRect(x: (b.width - w) / 2, y: (b.height - h) / 2, width: w, height: h)
    }

    /// Moves the pointer by a distance in screen points.
    private func movePointer(dx: CGFloat, dy: CGFloat) {
        let r = contentRect
        guard r.width > 0, r.height > 0 else { return }
        pointer.x = min(max(pointer.x + dx / r.width, 0), 1)
        pointer.y = min(max(pointer.y + dy / r.height, 0), 1)
        placeCursor()
        pendingMove = ["t": "m", "x": Double(pointer.x), "y": Double(pointer.y)]
        flushMove()
    }

    /// Only the newest position matters, so while the lossy channel is
    /// backed up, hold just the latest one.
    private func flushMove() {
        guard let pendingMove, let s = session, let ch = s.move, ch.bufferedAmount < 256 else { return }
        s.send(ch, pendingMove)
        self.pendingMove = nil
    }

    private func placeCursor() {
        let r = contentRect
        let p = CGPoint(x: r.minX + pointer.x * r.width, y: r.minY + pointer.y * r.height)
        cursorView.frame.origin = CGPoint(x: p.x - cursorHotspot.x, y: p.y - cursorHotspot.y)
        cursorView.isHidden = cursorHidden || videoTrack == nil
    }

    private func button(_ b: Int, down: Bool) {
        session?.send(session?.input, ["t": down ? "d" : "u", "b": b, "x": Double(pointer.x), "y": Double(pointer.y)])
    }

    private func wheel(dx: CGFloat, dy: CGFloat) {
        guard dx != 0 || dy != 0 else { return }
        session?.send(session?.input, ["t": "w", "dx": Double(dx), "dy": Double(dy)])
    }

    private func key(_ code: String, down: Bool, repeat r: Bool = false) {
        var m: [String: Any] = ["t": down ? "kd" : "ku", "c": code]
        if r { m["r"] = true }
        session?.send(session?.input, m)
    }

    private func releaseAll() {
        repeatKey = nil
        leftHeld = false
        session?.send(session?.input, ["t": "reset"])
    }

    // MARK: cursor shapes

    private func setCursor(css: String) {
        let image: UIImage?
        var hotspot: CGPoint
        let cfg = UIImage.SymbolConfiguration(pointSize: 30, weight: .semibold)
        func symbol(_ name: String) -> UIImage? {
            UIImage(systemName: name, withConfiguration: cfg)?.withTintColor(.white, renderingMode: .alwaysOriginal)
        }
        switch css {
        case "none":
            cursorHidden = true
            placeCursor()
            return
        case "pointer":
            image = symbol("hand.point.up.left.fill"); hotspot = CGPoint(x: 6, y: 2)
        case "text", "vertical-text":
            image = symbol("character.cursor.ibeam"); hotspot = .zero
        case "grab", "grabbing":
            image = symbol("hand.raised.fill"); hotspot = .zero
        case "crosshair", "cell":
            image = symbol("plus"); hotspot = .zero
        case "not-allowed", "no-drop":
            image = symbol("nosign"); hotspot = .zero
        case "move", "all-scroll":
            image = symbol("arrow.up.and.down.and.arrow.left.and.right"); hotspot = .zero
        case "ew-resize", "col-resize", "e-resize", "w-resize":
            image = symbol("arrow.left.and.right"); hotspot = .zero
        case "ns-resize", "row-resize", "n-resize", "s-resize":
            image = symbol("arrow.up.and.down"); hotspot = .zero
        case "nwse-resize", "nw-resize", "se-resize":
            image = symbol("arrow.up.left.and.arrow.down.right"); hotspot = .zero
        case "nesw-resize", "ne-resize", "sw-resize":
            image = symbol("arrow.up.right.and.arrow.down.left"); hotspot = .zero
        default:
            image = Self.arrow; hotspot = CGPoint(x: 2, y: 2)
        }
        guard let image else { return setCursor(css: "default") }
        if hotspot == .zero { hotspot = CGPoint(x: image.size.width / 2, y: image.size.height / 2) }
        show(cursor: image, hotspot: hotspot)
    }

    private func show(cursor image: UIImage, hotspot: CGPoint) {
        cursorHidden = false
        cursorView.image = image
        cursorView.frame.size = image.size
        cursorHotspot = hotspot
        placeCursor()
    }

    /// The host's own cursor image, sized like it is on the host.
    private func pngCursor(_ data: Data, x: Int, y: Int) -> (UIImage, CGPoint)? {
        guard let img = UIImage(data: data), img.size.width > 0 else { return nil }
        let hostWidth = CGFloat(source.width > 0 ? source.width : Int(frameSize.width))
        var scale = hostWidth > 0 ? contentRect.width / hostWidth : 1
        scale = min(max(scale, 0.75), 2.5)
        let size = CGSize(width: img.size.width * scale, height: img.size.height * scale)
        let scaled = UIGraphicsImageRenderer(size: size).image { _ in img.draw(in: CGRect(origin: .zero, size: size)) }
        return (scaled, CGPoint(x: CGFloat(x) * scale, y: CGFloat(y) * scale))
    }

    private static let arrow: UIImage = {
        let size = CGSize(width: 26, height: 38)
        return UIGraphicsImageRenderer(size: size).image { ctx in
            let p = UIBezierPath()
            p.move(to: CGPoint(x: 2, y: 2))
            p.addLine(to: CGPoint(x: 2, y: 30))
            p.addLine(to: CGPoint(x: 9, y: 23.5))
            p.addLine(to: CGPoint(x: 14, y: 35))
            p.addLine(to: CGPoint(x: 19, y: 33))
            p.addLine(to: CGPoint(x: 14, y: 21.5))
            p.addLine(to: CGPoint(x: 23, y: 21.5))
            p.close()
            p.lineJoinStyle = .round
            UIColor.white.setFill()
            p.fill()
            UIColor.black.setStroke()
            p.lineWidth = 2
            p.stroke()
        }
    }()

    // MARK: devices

    private func attach(_ c: GCController) {
        if let pad = c.extendedGamepad {
            if padIndex[ObjectIdentifier(c)] == nil {
                let used = Set(padIndex.values)
                if let free = (0..<4).first(where: { !used.contains($0) }) {
                    padIndex[ObjectIdentifier(c)] = free
                }
            }
            _ = pad
        } else if let remote = c.microGamepad {
            attachRemote(remote)
        }
    }

    private func detach(_ c: GCController) {
        detachHandlers(c)
        if let i = padIndex.removeValue(forKey: ObjectIdentifier(c)) {
            session?.sendRaw(session?.pads, "{\"i\":\(i),\"gone\":true}")
            padSent[i] = nil
        }
    }

    private func detachHandlers(_ c: GCController) {
        guard c.extendedGamepad == nil, let r = c.microGamepad else { return }
        r.dpad.valueChangedHandler = nil
        r.buttonA.pressedChangedHandler = nil
        r.buttonX.pressedChangedHandler = nil
        r.buttonMenu.pressedChangedHandler = nil
    }

    private func attachRemote(_ r: GCMicroGamepad) {
        r.reportsAbsoluteDpadValues = true
        r.allowsRotation = false
        r.dpad.valueChangedHandler = { [weak self] _, x, y in
            self?.remoteTouch(x: CGFloat(x), y: CGFloat(y))
        }
        r.buttonA.pressedChangedHandler = { [weak self] _, _, pressed in
            guard let self, !self.menuOpen else { return }
            self.leftHeld = pressed
            self.button(0, down: pressed)
        }
        r.buttonX.pressedChangedHandler = { [weak self] _, _, pressed in
            guard let self, !self.menuOpen else { return }
            if pressed {
                self.scrolling = true
                self.scrolled = false
            } else {
                self.scrolling = false
                if !self.scrolled {
                    self.button(2, down: true)
                    self.button(2, down: false)
                }
            }
        }
        r.buttonMenu.pressedChangedHandler = { [weak self] _, _, pressed in
            if pressed { self?.showMenu() }
        }
    }

    /// Absolute touch positions on the remote's surface (-1...1, y up),
    /// turned into relative pointer motion like a laptop trackpad.
    private func remoteTouch(x: CGFloat, y: CGFloat) {
        guard !menuOpen else { return }
        if x == 0 && y == 0 {
            lastTouch = nil // finger lifted
            return
        }
        defer { lastTouch = CGPoint(x: x, y: y) }
        guard let last = lastTouch else { return }
        let dx = x - last.x, dy = y - last.y
        if scrolling {
            scrolled = true
            // Natural scrolling: the content follows the finger.
            wheel(dx: -dx * 1200, dy: dy * 1200)
            return
        }
        // A little acceleration: slow swipes are precise, fast ones cross the screen.
        let speed = hypot(dx, dy)
        let gain = 650 + 6000 * min(speed, 0.15)
        movePointer(dx: dx * gain, dy: -dy * gain)
    }

    private func attach(_ k: GCKeyboard) {
        k.keyboardInput?.keyChangedHandler = { [weak self] _, _, keyCode, pressed in
            DispatchQueue.main.async {
                guard let self, !self.menuOpen, let code = KeyCodes.code(for: keyCode) else { return }
                self.key(code, down: pressed)
                let modifier = code.hasPrefix("Shift") || code.hasPrefix("Control") || code.hasPrefix("Alt") || code.hasPrefix("Meta") || code == "CapsLock"
                if pressed && !modifier {
                    self.repeatKey = code
                    self.repeatAt = CACurrentMediaTime() + 0.5
                } else if !pressed && self.repeatKey == code {
                    self.repeatKey = nil
                }
            }
        }
    }

    private func attach(_ m: GCMouse) {
        guard let input = m.mouseInput else { return }
        input.mouseMovedHandler = { [weak self] _, dx, dy in
            DispatchQueue.main.async {
                guard let self, !self.menuOpen else { return }
                self.movePointer(dx: CGFloat(dx) * 1.5, dy: CGFloat(-dy) * 1.5)
            }
        }
        let buttons: [(GCControllerButtonInput?, Int)] = [(input.leftButton, 0), (input.middleButton, 1), (input.rightButton, 2)]
        for (b, n) in buttons {
            b?.pressedChangedHandler = { [weak self] _, _, pressed in
                DispatchQueue.main.async {
                    guard let self, !self.menuOpen else { return }
                    self.button(n, down: pressed)
                }
            }
        }
        input.scroll.valueChangedHandler = { [weak self] _, x, y in
            DispatchQueue.main.async {
                guard let self, !self.menuOpen else { return }
                self.wheel(dx: CGFloat(x) * 40, dy: CGFloat(-y) * 40)
            }
        }
    }

    /// Per display frame: gamepad state (sent whole when it changes, and
    /// every 250ms in case the lossy channel dropped the last one), the
    /// Menu+Options chord, and keyboard auto-repeat.
    @objc private func tick() {
        let now = CACurrentMediaTime()
        if let k = repeatKey, now >= repeatAt, !menuOpen {
            key(k, down: true, repeat: true)
            repeatAt = now + 1.0 / 30
        }
        var chord = false
        for c in GCController.controllers() {
            guard let g = c.extendedGamepad, let i = padIndex[ObjectIdentifier(c)] else { continue }
            if g.buttonMenu.isPressed && (g.buttonOptions?.isPressed ?? false) { chord = true }
            guard !menuOpen else { continue }
            let b: [Float] = [
                g.buttonA.value, g.buttonB.value, g.buttonX.value, g.buttonY.value,
                g.leftShoulder.value, g.rightShoulder.value, g.leftTrigger.value, g.rightTrigger.value,
                g.buttonOptions?.value ?? 0, g.buttonMenu.value,
                g.leftThumbstickButton?.value ?? 0, g.rightThumbstickButton?.value ?? 0,
                g.dpad.up.value, g.dpad.down.value, g.dpad.left.value, g.dpad.right.value,
                g.buttonHome?.value ?? 0,
            ]
            let a: [Float] = [g.leftThumbstick.xAxis.value, -g.leftThumbstick.yAxis.value,
                              g.rightThumbstick.xAxis.value, -g.rightThumbstick.yAxis.value]
            let fmt = { (v: Float) in String(format: "%.3f", v).replacingOccurrences(of: "-0.000", with: "0.000") }
            let msg = "{\"i\":\(i),\"b\":[\(b.map(fmt).joined(separator: ","))],\"a\":[\(a.map(fmt).joined(separator: ","))]}"
            if let prev = padSent[i], prev.0 == msg, now - prev.1 < 0.25 { continue }
            if session?.pads?.readyState == .open {
                session?.sendRaw(session?.pads, msg)
                padSent[i] = (msg, now)
            }
        }
        if chord {
            if let since = menuChordSince {
                if now - since > 1 { menuChordSince = .infinity; showMenu() }
            } else {
                menuChordSince = now
            }
        } else {
            menuChordSince = nil
        }
    }

    // MARK: menu

    private func showMenu() {
        guard !menuOpen, presentedViewController == nil else { return }
        menuOpen = true
        releaseAll()
        controllerUserInteractionEnabled = true
        let sheet = UIAlertController(title: source.title, message: api.server.name, preferredStyle: .actionSheet)
        sheet.addAction(UIAlertAction(title: "Type Text…", style: .default) { [weak self] _ in self?.typeText() })
        sheet.addAction(UIAlertAction(title: "Send Key…", style: .default) { [weak self] _ in self?.sendKeyMenu() })
        sheet.addAction(UIAlertAction(title: statsLabel.isHidden ? "Show Stats" : "Hide Stats", style: .default) { [weak self] _ in
            self?.toggleStats()
            self?.menuClosed()
        })
        sheet.addAction(UIAlertAction(title: "Disconnect", style: .destructive) { [weak self] _ in self?.close() })
        sheet.addAction(UIAlertAction(title: "Cancel", style: .cancel) { [weak self] _ in self?.menuClosed() })
        present(sheet, animated: true)
    }

    private func menuClosed() {
        menuOpen = false
        controllerUserInteractionEnabled = false
        lastTouch = nil
    }

    private func typeText() {
        let alert = UIAlertController(title: "Type Text", message: "Typed on the host with a US keyboard layout.", preferredStyle: .alert)
        alert.addTextField { $0.autocorrectionType = .no; $0.autocapitalizationType = .none }
        alert.addAction(UIAlertAction(title: "Type", style: .default) { [weak self, weak alert] _ in
            self?.menuClosed()
            self?.type(alert?.textFields?.first?.text ?? "")
        })
        alert.addAction(UIAlertAction(title: "Type and Press Enter", style: .default) { [weak self, weak alert] _ in
            self?.menuClosed()
            self?.type((alert?.textFields?.first?.text ?? "") + "\n")
        })
        alert.addAction(UIAlertAction(title: "Cancel", style: .cancel) { [weak self] _ in self?.menuClosed() })
        present(alert, animated: true)
    }

    private func type(_ text: String) {
        for ch in text {
            guard let (code, shift) = KeyCodes.type(ch) else { continue }
            if shift { key("ShiftLeft", down: true) }
            key(code, down: true)
            key(code, down: false)
            if shift { key("ShiftLeft", down: false) }
        }
    }

    private func sendKeyMenu() {
        let combos: [(String, [String])] = [
            ("Escape", ["Escape"]), ("Enter", ["Enter"]), ("Tab", ["Tab"]), ("Backspace", ["Backspace"]),
            ("Alt+Tab", ["AltLeft", "Tab"]), ("Windows / Command", ["MetaLeft"]),
            ("Command+Tab", ["MetaLeft", "Tab"]), ("Ctrl+Alt+Delete", ["ControlLeft", "AltLeft", "Delete"]),
            ("Alt+F4", ["AltLeft", "F4"]),
        ]
        let sheet = UIAlertController(title: "Send Key", message: nil, preferredStyle: .actionSheet)
        for (title, keys) in combos {
            sheet.addAction(UIAlertAction(title: title, style: .default) { [weak self] _ in
                guard let self else { return }
                self.menuClosed()
                keys.forEach { self.key($0, down: true) }
                keys.reversed().forEach { self.key($0, down: false) }
            })
        }
        sheet.addAction(UIAlertAction(title: "Cancel", style: .cancel) { [weak self] _ in self?.menuClosed() })
        present(sheet, animated: true)
    }

    // MARK: stats

    private func toggleStats() {
        statsLabel.isHidden.toggle()
        statsTimer?.invalidate()
        statsTimer = nil
        lastStats = nil
        guard !statsLabel.isHidden else { return }
        statsLabel.text = " … "
        view.setNeedsLayout()
        statsTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.updateStats() }
        }
    }

    private func updateStats() async {
        guard let s = await session?.stats() else { return }
        defer { lastStats = s }
        guard let p = lastStats, s.timestamp > p.timestamp else { return }
        let dt = s.timestamp - p.timestamp
        let fps = (s.framesDecoded - p.framesDecoded) / dt
        let mbps = (s.bytes - p.bytes) * 8 / dt / 1e6
        let de = s.emitted - p.emitted
        let jb = de > 0 ? (s.jitterBuffer - p.jitterBuffer) / de * 1000 : 0
        let df = s.framesDecoded - p.framesDecoded
        let dec = df > 0 ? (s.decodeTime - p.decodeTime) / df * 1000 : 0
        statsLabel.text = String(format: " %@ %d×%d %.0ffps %.1fMbps \n rtt %.0fms  jitbuf %.0fms  dec %.1fms  lost %d ",
                                 s.codec, s.width, s.height, fps, mbps, s.rtt, jb, dec, s.lost)
        view.setNeedsLayout()
    }
}

extension StreamViewController: SessionDelegate {
    func session(_ s: Session, didReceive video: LKRTCVideoTrack) {
        guard s === session else { return }
        videoTrack = video
        video.add(videoView)
        placeCursor()
    }

    func session(_ s: Session, didChange state: LKRTCPeerConnectionState) {
        guard s === session, !closed else { return }
        switch state {
        case .connected:
            toast(nil)
        case .failed:
            toast("Connection failed — reconnecting…")
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak self] in
                guard let self, s === self.session else { return }
                self.reconnect()
            }
        case .disconnected:
            toast("Connection lost — reconnecting…")
            // "disconnected" often heals by itself; give it a moment.
            DispatchQueue.main.asyncAfter(deadline: .now() + 4) { [weak self] in
                guard let self, s === self.session, self.statusLabel.isHidden == false else { return }
                self.reconnect()
            }
        default:
            break
        }
    }

    func session(_ s: Session, didReceiveControl m: [String: Any]) {
        guard s === session else { return }
        switch m["t"] as? String {
        case "ended":
            close(message: "\(source.title) was closed")
        case "notice":
            if let msg = m["msg"] as? String {
                toast(msg)
                DispatchQueue.main.asyncAfter(deadline: .now() + 8) { [weak self] in
                    if self?.statusLabel.text == msg { self?.toast(nil) }
                }
            }
        case "cursor":
            if m["hidden"] as? Bool == true {
                cursorHidden = true
                placeCursor()
                return
            }
            let id = (m["id"] as? NSNumber)?.uint64Value
            if let css = m["css"] as? String {
                if let id { cursorCSS[id] = css }
                setCursor(css: css)
                return
            }
            if let b64 = m["png"] as? String, let data = Data(base64Encoded: b64),
               let c = pngCursor(data, x: (m["x"] as? NSNumber)?.intValue ?? 0, y: (m["y"] as? NSNumber)?.intValue ?? 0) {
                if let id { cursors[id] = c }
                show(cursor: c.0, hotspot: c.1)
                return
            }
            if let id, let c = cursors[id] {
                show(cursor: c.0, hotspot: c.1)
            } else if let id, let css = cursorCSS[id] {
                setCursor(css: css)
            } else {
                setCursor(css: "default")
            }
        default:
            break
        }
    }

    func sessionMoveChannelDrained(_ s: Session) {
        guard s === session else { return }
        flushMove()
    }
}

extension Notification.Name {
    static let urdpNotice = Notification.Name("urdpNotice")
}
