import UIKit
import AVFoundation
import LiveKitWebRTC

/// Shows decoded WebRTC frames through AVSampleBufferDisplayLayer, each
/// marked for immediate display so nothing is queued behind it.
final class VideoView: UIView, LKRTCVideoRenderer {
    override class var layerClass: AnyClass { AVSampleBufferDisplayLayer.self }
    private var displayLayer: AVSampleBufferDisplayLayer { layer as! AVSampleBufferDisplayLayer }

    /// Called on the main queue when the decoded frame size changes.
    var onSizeChange: ((CGSize) -> Void)?

    private var format: CMVideoFormatDescription?
    private var size = CGSize.zero
    private var pool: CVPixelBufferPool?
    private var poolSize = CGSize.zero

    override init(frame: CGRect) {
        super.init(frame: frame)
        displayLayer.videoGravity = .resizeAspect
        backgroundColor = .black
    }

    required init?(coder: NSCoder) { fatalError() }

    func setSize(_ size: CGSize) {}

    func renderFrame(_ frame: LKRTCVideoFrame?) {
        guard let frame, let pixelBuffer = pixelBuffer(for: frame) else { return }
        let w = CVPixelBufferGetWidth(pixelBuffer), h = CVPixelBufferGetHeight(pixelBuffer)
        let newSize = CGSize(width: w, height: h)
        if newSize != size {
            size = newSize
            DispatchQueue.main.async { [weak self] in self?.onSizeChange?(newSize) }
        }
        if format == nil || !CMVideoFormatDescriptionMatchesImageBuffer(format!, imageBuffer: pixelBuffer) {
            format = nil
            CMVideoFormatDescriptionCreateForImageBuffer(allocator: nil, imageBuffer: pixelBuffer, formatDescriptionOut: &format)
        }
        guard let format else { return }
        var timing = CMSampleTimingInfo(duration: .invalid, presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()), decodeTimeStamp: .invalid)
        var sample: CMSampleBuffer?
        CMSampleBufferCreateReadyWithImageBuffer(allocator: nil, imageBuffer: pixelBuffer, formatDescription: format,
                                                 sampleTiming: &timing, sampleBufferOut: &sample)
        guard let sample else { return }
        if let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: true),
           CFArrayGetCount(attachments) > 0 {
            let dict = unsafeBitCast(CFArrayGetValueAtIndex(attachments, 0), to: CFMutableDictionary.self)
            CFDictionarySetValue(dict, Unmanaged.passUnretained(kCMSampleAttachmentKey_DisplayImmediately).toOpaque(),
                                 Unmanaged.passUnretained(kCFBooleanTrue).toOpaque())
        }
        let renderer = displayLayer.sampleBufferRenderer
        if renderer.status == .failed || renderer.requiresFlushToResumeDecoding {
            renderer.flush()
        }
        renderer.enqueue(sample)
    }

    /// Hardware-decoded frames already are CVPixelBuffers; software-decoded
    /// ones (VP8, VP9, AV1) are I420 and get copied into an NV12 buffer.
    private func pixelBuffer(for frame: LKRTCVideoFrame) -> CVPixelBuffer? {
        if let cv = frame.buffer as? LKRTCCVPixelBuffer {
            return cv.pixelBuffer
        }
        let i420 = frame.buffer.toI420()
        let w = Int(i420.width), h = Int(i420.height)
        if pool == nil || poolSize != CGSize(width: w, height: h) {
            let attrs: [String: Any] = [
                kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
                kCVPixelBufferWidthKey as String: w,
                kCVPixelBufferHeightKey as String: h,
                kCVPixelBufferIOSurfacePropertiesKey as String: [:],
            ]
            pool = nil
            CVPixelBufferPoolCreate(nil, nil, attrs as CFDictionary, &pool)
            poolSize = CGSize(width: w, height: h)
        }
        guard let pool else { return nil }
        var out: CVPixelBuffer?
        CVPixelBufferPoolCreatePixelBuffer(nil, pool, &out)
        guard let out else { return nil }
        CVPixelBufferLockBaseAddress(out, [])
        defer { CVPixelBufferUnlockBaseAddress(out, []) }
        let y = CVPixelBufferGetBaseAddressOfPlane(out, 0)!.assumingMemoryBound(to: UInt8.self)
        let yStride = CVPixelBufferGetBytesPerRowOfPlane(out, 0)
        for row in 0..<h {
            memcpy(y + row * yStride, i420.dataY + row * Int(i420.strideY), w)
        }
        let uv = CVPixelBufferGetBaseAddressOfPlane(out, 1)!.assumingMemoryBound(to: UInt8.self)
        let uvStride = CVPixelBufferGetBytesPerRowOfPlane(out, 1)
        let cw = (w + 1) / 2, ch = (h + 1) / 2
        for row in 0..<ch {
            let u = i420.dataU + row * Int(i420.strideU), v = i420.dataV + row * Int(i420.strideV)
            let dst = uv + row * uvStride
            for col in 0..<cw {
                dst[col * 2] = u[col]
                dst[col * 2 + 1] = v[col]
            }
        }
        return out
    }
}
