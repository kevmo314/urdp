#import "vt_darwin.h"
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>

// One VideoToolbox compression session. Exactly one frame is in flight at a
// time: ur_vt_encode submits it and blocks in CompleteFrames until its
// callback has run, so the output callback (on a VT thread) only ever writes
// fields that the caller reads after CompleteFrames returns.
struct ur_vt {
  VTCompressionSessionRef session;
  BOOL hevc;
  int fps, width, height;
  int64_t last_pts;
  // Written by the output callback for the frame in flight.
  intptr_t seq, got_seq;
  int callbacks;
  OSStatus status;
  VTEncodeInfoFlags flags;
  CMSampleBufferRef sample;
  // Annex B output, reused between frames.
  uint8_t *out;
  size_t out_len, out_cap;
  uint8_t *tmp;
  size_t tmp_cap;
};

// Hardware only. kVTVideoEncoderSpecification_EnableLowLatencyRateControl
// is deliberately not used: measured on an M1 (macOS 27) it ignores
// DataRateLimits, so a keyframe can come out at 40x the per-frame budget
// (164 KB at 2 Mbit/s 1080p60), after which it drops up to 3 of every 4
// frames to pay the debt back; it also rejects MaxFrameDelayCount and
// PrioritizeEncodingSpeedOverQuality. The regular real-time encoder below
// is as fast per frame, returns every frame synchronously, never dropped a
// frame in testing, and keeps keyframes within the rate limit.
static NSDictionary *specification(void) {
  return @{
    (__bridge NSString *)
    kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder : @YES
  };
}

int ur_vt_supported(int hevc) {
  @autoreleasepool {
    CFDictionaryRef properties = NULL;
    OSStatus status = VTCopySupportedPropertyDictionaryForEncoder(
        1920, 1080, hevc ? kCMVideoCodecType_HEVC : kCMVideoCodecType_H264,
        (__bridge CFDictionaryRef)specification(), NULL, &properties);
    if (properties)
      CFRelease(properties);
    return status == noErr;
  }
}

static void output(void *ref, void *frame, OSStatus status,
                   VTEncodeInfoFlags flags, CMSampleBufferRef sample) {
  ur_vt *e = ref;
  e->callbacks++;
  e->got_seq = (intptr_t)frame;
  e->status = status;
  e->flags = flags;
  if (e->sample) {
    CFRelease(e->sample);
    e->sample = NULL;
  }
  if (!status && !(flags & kVTEncodeInfo_FrameDropped) && sample)
    e->sample = (CMSampleBufferRef)CFRetain(sample);
}

int ur_vt_bitrate(ur_vt *e, int kbps) {
  @autoreleasepool {
    int64_t bps = (int64_t)kbps * 1000;
    OSStatus status =
        VTSessionSetProperty(e->session, kVTCompressionPropertyKey_AverageBitRate,
                             (__bridge CFNumberRef) @(bps));
    if (status)
      return status;
    // Hard limit of 1.5x the average over any second (as libwebrtc does), so
    // a keyframe or burst can't overshoot the congestion-controlled rate for
    // long. A 1x limit undershot the target by ~30% in testing.
    return VTSessionSetProperty(
        e->session, kVTCompressionPropertyKey_DataRateLimits,
        (__bridge CFArrayRef) @[ @(bps * 3 / 16), @1 ]);
  }
}

void ur_vt_close(ur_vt *e) {
  if (!e)
    return;
  if (e->session) {
    VTCompressionSessionInvalidate(e->session);
    CFRelease(e->session);
  }
  if (e->sample)
    CFRelease(e->sample);
  free(e->out);
  free(e->tmp);
  free(e);
}

// set applies a property; optional ones may be unsupported by an encoder.
static OSStatus set(ur_vt *e, CFStringRef key, CFTypeRef value, BOOL optional) {
  OSStatus status = VTSessionSetProperty(e->session, key, value);
  if (optional && status == kVTPropertyNotSupportedErr)
    return noErr;
  return status;
}

static OSStatus create(ur_vt *e) {
  NSDictionary *attrs = @{
    (__bridge NSString *)kCVPixelBufferPixelFormatTypeKey :
        @(kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange),
    (__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey : @{}
  };
  return VTCompressionSessionCreate(
      NULL, e->width, e->height,
      e->hevc ? kCMVideoCodecType_HEVC : kCMVideoCodecType_H264,
      (__bridge CFDictionaryRef)specification(),
      (__bridge CFDictionaryRef)attrs, NULL, output, e, &e->session);
}

ur_vt *ur_vt_open(int w, int h, int fps, int kbps, int keyint_frames,
                  int keyint_seconds, int hevc, int *status) {
  @autoreleasepool {
    ur_vt *e = calloc(1, sizeof(*e));
    e->hevc = hevc;
    e->fps = fps;
    e->width = w;
    e->height = h;
    e->last_pts = INT64_MIN;
    *status = create(e);
    if (*status) {
      ur_vt_close(e);
      return NULL;
    }
    CFStringRef profile =
        hevc ? kVTProfileLevel_HEVC_Main_AutoLevel
             // Matches the profile-level-id=42e01f we offer in SDP.
             : kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel;
    struct {
      CFStringRef key;
      CFTypeRef value;
      BOOL optional;
    } props[] = {
        // Required for latency: encode as frames arrive, never reorder.
        {kVTCompressionPropertyKey_RealTime, kCFBooleanTrue, NO},
        {kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse, NO},
        {kVTCompressionPropertyKey_ProfileLevel, profile, NO},
        {kVTCompressionPropertyKey_MaxKeyFrameInterval,
         (__bridge CFNumberRef) @(keyint_frames), NO},
        {kVTCompressionPropertyKey_ColorPrimaries,
         kCVImageBufferColorPrimaries_ITU_R_709_2, NO},
        {kVTCompressionPropertyKey_TransferFunction,
         kCVImageBufferTransferFunction_ITU_R_709_2, NO},
        {kVTCompressionPropertyKey_YCbCrMatrix,
         kCVImageBufferYCbCrMatrix_ITU_R_709_2, NO},
        // Don't hold frames for lookahead. Supported by the M1 encoder;
        // optional in case older ones lack it: Encode's CompleteFrames
        // flushes each frame out regardless.
        {kVTCompressionPropertyKey_MaxFrameDelayCount, (__bridge CFNumberRef) @0,
         YES},
        {kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality,
         kCFBooleanTrue, YES},
        {kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration,
         (__bridge CFNumberRef) @(keyint_seconds), YES},
        {kVTCompressionPropertyKey_ExpectedFrameRate,
         (__bridge CFNumberRef) @(fps), YES},
    };
    for (size_t i = 0; i < sizeof(props) / sizeof(props[0]) && !*status; i++)
      *status = set(e, props[i].key, props[i].value, props[i].optional);
    if (!*status)
      *status = ur_vt_bitrate(e, kbps);
    if (!*status)
      *status = VTCompressionSessionPrepareToEncodeFrames(e->session);
    if (*status) {
      ur_vt_close(e);
      return NULL;
    }
    return e;
  }
}

int ur_vt_property(ur_vt *e, const char *name, int64_t *value) {
  @autoreleasepool {
    NSDictionary *keys = @{
      @"RealTime" : (__bridge NSString *)kVTCompressionPropertyKey_RealTime,
      @"AllowFrameReordering" :
          (__bridge NSString *)kVTCompressionPropertyKey_AllowFrameReordering,
      @"MaxFrameDelayCount" :
          (__bridge NSString *)kVTCompressionPropertyKey_MaxFrameDelayCount,
      @"PrioritizeEncodingSpeedOverQuality" : (__bridge NSString *)
          kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality,
      @"AverageBitRate" :
          (__bridge NSString *)kVTCompressionPropertyKey_AverageBitRate,
      @"Hardware" : (__bridge NSString *)
          kVTCompressionPropertyKey_UsingHardwareAcceleratedVideoEncoder,
    };
    NSString *key = keys[@(name)];
    if (!key)
      return kVTPropertyNotSupportedErr;
    CFTypeRef v = NULL;
    OSStatus status = VTSessionCopyProperty(
        e->session, (__bridge CFStringRef)key, NULL, &v);
    if (status)
      return status;
    if (!v)
      return kVTPropertyNotSupportedErr;
    id obj = CFBridgingRelease(v);
    if (![obj isKindOfClass:[NSNumber class]])
      return kVTParameterErr;
    *value = [obj longLongValue];
    return noErr;
  }
}

void *ur_vt_pixel(int w, int h) {
  @autoreleasepool {
    CVPixelBufferRef pixel = NULL;
    CVPixelBufferCreate(
        NULL, w, h, kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
        (__bridge CFDictionaryRef)
            @{(__bridge NSString *)kCVPixelBufferIOSurfacePropertiesKey : @{}},
        &pixel);
    if (pixel && !CVPixelBufferGetIOSurface(pixel)) {
      CVPixelBufferRelease(pixel);
      return NULL;
    }
    return pixel;
  }
}

void ur_vt_release(void *pixel) {
  if (pixel)
    CVPixelBufferRelease(pixel);
}

int ur_vt_upload(void *pixel, const uint8_t *y, int ys, const uint8_t *uv,
                 int uvs) {
  CVPixelBufferRef p = pixel;
  CVReturn status = CVPixelBufferLockBaseAddress(p, 0);
  if (status)
    return status;
  size_t w = CVPixelBufferGetWidth(p), h = CVPixelBufferGetHeight(p);
  for (int plane = 0; plane < 2; plane++) {
    uint8_t *dst = CVPixelBufferGetBaseAddressOfPlane(p, plane);
    size_t stride = CVPixelBufferGetBytesPerRowOfPlane(p, plane);
    const uint8_t *src = plane ? uv : y;
    int ss = plane ? uvs : ys;
    for (size_t row = 0; row < (plane ? h / 2 : h); row++)
      memcpy(dst + row * stride, src + row * ss, w);
  }
  return CVPixelBufferUnlockBaseAddress(p, 0);
}

static int reserve(uint8_t **buf, size_t *cap, size_t need) {
  if (need <= *cap)
    return 1;
  size_t n = MAX(need, *cap * 2);
  uint8_t *p = realloc(*buf, n);
  if (!p)
    return 0;
  *buf = p;
  *cap = n;
  return 1;
}

static int append_nal(ur_vt *e, const uint8_t *bytes, size_t size) {
  if (!reserve(&e->out, &e->out_cap, e->out_len + 4 + size))
    return 0;
  uint8_t *d = e->out + e->out_len;
  d[0] = d[1] = d[2] = 0;
  d[3] = 1;
  memcpy(d + 4, bytes, size);
  e->out_len += 4 + size;
  return 1;
}

typedef OSStatus (*param_set_fn)(CMFormatDescriptionRef, size_t,
                                 const uint8_t **, size_t *, size_t *, int *);

// annexb converts e->sample from length-prefixed (AVCC/HVCC) NAL units to
// start codes, prefixed by the parameter sets on keyframes.
static OSStatus annexb(ur_vt *e, int is_key) {
  e->out_len = 0;
  CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(e->sample);
  param_set_fn get = e->hevc ? CMVideoFormatDescriptionGetHEVCParameterSetAtIndex
                             : CMVideoFormatDescriptionGetH264ParameterSetAtIndex;
  int length_size = 0;
  size_t count = 0;
  const uint8_t *param;
  size_t param_size;
  OSStatus status = get(fmt, 0, &param, &param_size, &count, &length_size);
  if (status)
    return status;
  if (length_size < 1 || length_size > 4)
    return kVTParameterErr;
  if (is_key) {
    for (size_t i = 0; i < count; i++) {
      if ((status = get(fmt, i, &param, &param_size, NULL, NULL)))
        return status;
      if (!append_nal(e, param, param_size))
        return kVTAllocationFailedErr;
    }
  }
  CMBlockBufferRef block = CMSampleBufferGetDataBuffer(e->sample);
  if (!block)
    return kVTParameterErr;
  size_t total = CMBlockBufferGetDataLength(block), contiguous = 0;
  char *ptr = NULL;
  status = CMBlockBufferGetDataPointer(block, 0, &contiguous, NULL, &ptr);
  if (status || contiguous < total) {
    // Rare: the block buffer is fragmented; flatten it.
    if (!reserve(&e->tmp, &e->tmp_cap, total))
      return kVTAllocationFailedErr;
    if ((status = CMBlockBufferCopyDataBytes(block, 0, total, e->tmp)))
      return status;
    ptr = (char *)e->tmp;
  }
  const uint8_t *bytes = (const uint8_t *)ptr;
  for (size_t pos = 0; pos < total;) {
    if (total - pos < (size_t)length_size)
      return kVTParameterErr;
    uint32_t n = 0;
    for (int j = 0; j < length_size; j++)
      n = (n << 8) | bytes[pos++];
    if (!n || n > total - pos)
      return kVTParameterErr;
    if (!append_nal(e, bytes + pos, n))
      return kVTAllocationFailedErr;
    pos += n;
  }
  return noErr;
}

int ur_vt_encode(ur_vt *e, void *pixel, int64_t pts_ms, int key, void **data,
                 size_t *size, int *is_key) {
  @autoreleasepool {
    *size = 0;
    *is_key = 0;
    CVPixelBufferRef p = pixel;
    if (CVPixelBufferGetPixelFormatType(p) !=
            kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange ||
        (int)CVPixelBufferGetWidth(p) != e->width ||
        (int)CVPixelBufferGetHeight(p) != e->height)
      return kVTParameterErr;
    // VideoToolbox rejects non-increasing timestamps.
    if (pts_ms <= e->last_pts)
      pts_ms = e->last_pts + 1;
    e->last_pts = pts_ms;
    if (e->sample) {
      CFRelease(e->sample);
      e->sample = NULL;
    }
    e->seq++;
    e->got_seq = 0;
    e->callbacks = 0;
    e->status = noErr;
    e->flags = 0;
    CMTime time = CMTimeMake(pts_ms, 1000);
    NSDictionary *opts =
        key ? @{(__bridge NSString *)kVTEncodeFrameOptionKey_ForceKeyFrame : @YES}
            : nil;
    // VT retains the pixel buffer for as long as it needs it; the caller's
    // reference (capture's held buffer) covers the call itself.
    OSStatus status = VTCompressionSessionEncodeFrame(
        e->session, p, time, CMTimeMake(1, e->fps),
        (__bridge CFDictionaryRef)opts, (void *)e->seq, NULL);
    if (status)
      return status;
    // Block until this frame's callback has run: one in, one out.
    if ((status = VTCompressionSessionCompleteFrames(e->session, time)))
      return status;
    if (e->callbacks != 1 || e->got_seq != e->seq)
      return kVTVideoEncoderMalfunctionErr;
    if (e->status)
      return e->status;
    if (!e->sample) // dropped
      return noErr;
    NSArray *attachments =
        (__bridge NSArray *)CMSampleBufferGetSampleAttachmentsArray(e->sample,
                                                                    false);
    *is_key = ![attachments.firstObject[(
        __bridge NSString *)kCMSampleAttachmentKey_NotSync] boolValue];
    if ((status = annexb(e, *is_key)))
      return status;
    *data = e->out;
    *size = e->out_len;
    return noErr;
  }
}
