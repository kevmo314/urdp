import Foundation
import AVFoundation
import LiveKitWebRTC

/// Events a Session reports to its owner, always on the main queue.
protocol SessionDelegate: AnyObject {
    func session(_ s: Session, didReceive video: LKRTCVideoTrack)
    func session(_ s: Session, didChange state: LKRTCPeerConnectionState)
    func session(_ s: Session, didReceiveControl message: [String: Any])
    func sessionMoveChannelDrained(_ s: Session)
}

/// One WebRTC connection to a urdp source, negotiated the way the web
/// client does it: recvonly video and audio, the "input", "move" and
/// "gamepad" data channels, and a single non-trickle offer/answer.
final class Session: NSObject {
    static let factory: LKRTCPeerConnectionFactory = {
        LKRTCInitializeSSL()
        // AudioEngine playout without voice processing: there is no
        // microphone to cancel echo for, and playback stays full band.
        return LKRTCPeerConnectionFactory(audioDeviceModuleType: .audioEngine,
                                          bypassVoiceProcessing: true,
                                          encoderFactory: LKRTCDefaultVideoEncoderFactory(),
                                          decoderFactory: LKRTCDefaultVideoDecoderFactory(),
                                          audioProcessingModule: nil)
    }()

    weak var delegate: SessionDelegate?
    let api: API
    let source: String

    private var pc: LKRTCPeerConnection?
    private(set) var input: LKRTCDataChannel?
    private(set) var move: LKRTCDataChannel?
    private(set) var pads: LKRTCDataChannel?
    private var gathered: CheckedContinuation<Void, Never>?

    init(api: API, source: String) {
        self.api = api
        self.source = source
    }

    func connect() async throws {
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .moviePlayback)
        try? AVAudioSession.sharedInstance().setActive(true)

        let cfg = LKRTCConfiguration()
        cfg.sdpSemantics = .unifiedPlan
        cfg.bundlePolicy = .maxBundle
        cfg.continualGatheringPolicy = .gatherOnce
        let constraints = LKRTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let pc = Session.factory.peerConnection(with: cfg, constraints: constraints, delegate: self) else {
            throw APIError(message: "Couldn't create a peer connection")
        }
        self.pc = pc

        let recv = LKRTCRtpTransceiverInit()
        recv.direction = .recvOnly
        pc.addTransceiver(of: .video, init: recv)
        pc.addTransceiver(of: .audio, init: recv)

        let lossy = LKRTCDataChannelConfiguration()
        lossy.isOrdered = false
        lossy.maxRetransmits = 0
        let reliable = LKRTCDataChannelConfiguration()
        reliable.isOrdered = true
        pads = pc.dataChannel(forLabel: "gamepad", configuration: lossy)
        input = pc.dataChannel(forLabel: "input", configuration: reliable)
        move = pc.dataChannel(forLabel: "move", configuration: lossy)
        input?.delegate = self
        move?.delegate = self

        let offer: LKRTCSessionDescription = try await withCheckedThrowingContinuation { c in
            pc.offer(for: constraints) { sdp, err in
                if let sdp { c.resume(returning: sdp) } else { c.resume(throwing: err ?? APIError(message: "createOffer failed")) }
            }
        }
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            pc.setLocalDescription(offer) { err in
                if let err { c.resume(throwing: err) } else { c.resume() }
            }
        }
        await gatheringComplete()
        guard let local = pc.localDescription else { throw APIError(message: "No local description") }
        let answer = try await api.offer(source: source, sdp: local.sdp)
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            pc.setRemoteDescription(LKRTCSessionDescription(type: .answer, sdp: answer)) { err in
                if let err { c.resume(throwing: err) } else { c.resume() }
            }
        }
        for t in pc.transceivers where t.mediaType == .video {
            if let track = t.receiver.track as? LKRTCVideoTrack {
                DispatchQueue.main.async { self.delegate?.session(self, didReceive: track) }
            }
        }
    }

    /// Waits for ICE gathering, but no more than a second: host candidates
    /// arrive almost at once, and slow STUN shouldn't hold up the offer.
    private func gatheringComplete() async {
        if pc?.iceGatheringState == .complete { return }
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            DispatchQueue.main.async {
                self.gathered = c
                DispatchQueue.main.asyncAfter(deadline: .now() + 1) { self.finishGathering() }
            }
        }
    }

    private func finishGathering() {
        gathered?.resume()
        gathered = nil
    }

    func close() {
        input?.delegate = nil
        move?.delegate = nil
        pc?.close()
        pc = nil
        DispatchQueue.main.async { self.finishGathering() }
    }

    // MARK: sending

    func send(_ channel: LKRTCDataChannel?, _ message: [String: Any]) {
        guard let channel, channel.readyState == .open,
              let data = try? JSONSerialization.data(withJSONObject: message) else { return }
        channel.sendData(LKRTCDataBuffer(data: data, isBinary: false))
    }

    func sendRaw(_ channel: LKRTCDataChannel?, _ json: String) {
        guard let channel, channel.readyState == .open else { return }
        channel.sendData(LKRTCDataBuffer(data: Data(json.utf8), isBinary: false))
    }

    // MARK: stats

    struct Stats {
        var codec = "", width = 0, height = 0
        var framesDecoded = 0.0, bytes = 0.0, timestamp = 0.0
        var lost = 0, rtt = Double.nan, jitterBuffer = 0.0, emitted = 0.0, decodeTime = 0.0
    }

    func stats() async -> Stats? {
        guard let pc else { return nil }
        let report: LKRTCStatisticsReport = await withCheckedContinuation { c in
            pc.statistics { c.resume(returning: $0) }
        }
        var s = Stats()
        var codecId: String?
        var found = false
        for (_, st) in report.statistics {
            let v = st.values
            if st.type == "inbound-rtp", v["kind"] as? String == "video" {
                found = true
                s.width = (v["frameWidth"] as? NSNumber)?.intValue ?? 0
                s.height = (v["frameHeight"] as? NSNumber)?.intValue ?? 0
                s.framesDecoded = (v["framesDecoded"] as? NSNumber)?.doubleValue ?? 0
                s.bytes = (v["bytesReceived"] as? NSNumber)?.doubleValue ?? 0
                s.lost = (v["packetsLost"] as? NSNumber)?.intValue ?? 0
                s.jitterBuffer = (v["jitterBufferDelay"] as? NSNumber)?.doubleValue ?? 0
                s.emitted = (v["jitterBufferEmittedCount"] as? NSNumber)?.doubleValue ?? 0
                s.decodeTime = (v["totalDecodeTime"] as? NSNumber)?.doubleValue ?? 0
                s.timestamp = st.timestamp_us / 1e6
                codecId = v["codecId"] as? String
            }
            if st.type == "candidate-pair", v["nominated"] as? Bool == true, v["state"] as? String == "succeeded",
               let r = v["currentRoundTripTime"] as? NSNumber {
                s.rtt = r.doubleValue * 1000
            }
        }
        if let codecId, let c = report.statistics[codecId], let mime = c.values["mimeType"] as? String {
            s.codec = mime.replacingOccurrences(of: "video/", with: "")
        }
        return found ? s : nil
    }
}

extension Session: LKRTCPeerConnectionDelegate {
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange stateChanged: LKRTCSignalingState) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didAdd stream: LKRTCMediaStream) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didRemove stream: LKRTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ peerConnection: LKRTCPeerConnection) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange newState: LKRTCIceConnectionState) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange newState: LKRTCIceGatheringState) {
        if newState == .complete { DispatchQueue.main.async { self.finishGathering() } }
    }
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didGenerate candidate: LKRTCIceCandidate) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didRemove candidates: [LKRTCIceCandidate]) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didOpen dataChannel: LKRTCDataChannel) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange newState: LKRTCPeerConnectionState) {
        DispatchQueue.main.async { self.delegate?.session(self, didChange: newState) }
    }
}

extension Session: LKRTCDataChannelDelegate {
    func dataChannelDidChangeState(_ dataChannel: LKRTCDataChannel) {}

    func dataChannel(_ dataChannel: LKRTCDataChannel, didReceiveMessageWith buffer: LKRTCDataBuffer) {
        guard dataChannel === input,
              let obj = try? JSONSerialization.jsonObject(with: buffer.data) as? [String: Any] else { return }
        DispatchQueue.main.async { self.delegate?.session(self, didReceiveControl: obj) }
    }

    func dataChannel(_ dataChannel: LKRTCDataChannel, didChangeBufferedAmount amount: UInt64) {
        guard dataChannel === move else { return }
        DispatchQueue.main.async { self.delegate?.sessionMoveChannelDrained(self) }
    }
}
