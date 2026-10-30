#import <CoreAudio/CoreAudioTypes.h>
#import <CoreGraphics/CoreGraphics.h>
#import <CoreMedia/CoreMedia.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>

#define UR_AUDIO_CAPACITY 9600
@interface URAudio : NSObject <SCStreamOutput, SCStreamDelegate> {
@public
  int16_t samples[UR_AUDIO_CAPACITY];
@public
  size_t head, count;
}
@property SCStream *stream;
@property NSLock *lock;
@property NSString *failure;
@end
@implementation URAudio
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
  [self.lock lock];
  self.failure = error.localizedDescription;
  [self.lock unlock];
}
- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sample
                   ofType:(SCStreamOutputType)type {
  if (type != SCStreamOutputTypeAudio || !CMSampleBufferDataIsReady(sample))
    return;
  const AudioStreamBasicDescription *fmt =
      CMAudioFormatDescriptionGetStreamBasicDescription(
          CMSampleBufferGetFormatDescription(sample));
  if (!fmt || fmt->mSampleRate != 48000 || fmt->mChannelsPerFrame != 2 ||
      fmt->mFormatID != kAudioFormatLinearPCM) {
    [self.lock lock];
    self.failure = @"ScreenCaptureKit returned unsupported audio format "
                   @"(expected 48kHz stereo PCM)";
    [self.lock unlock];
    return;
  }
  size_t bytes = 0;
  CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
      sample, &bytes, NULL, 0, NULL, NULL, 0, NULL);
  AudioBufferList *list = malloc(bytes);
  CMBlockBufferRef block = NULL;
  OSStatus result = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
      sample, NULL, list, bytes, kCFAllocatorDefault, kCFAllocatorDefault,
      kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &block);
  if (result) {
    if (block)
      CFRelease(block);
    free(list);
    return;
  }
  BOOL planar = (fmt->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
  BOOL floating = (fmt->mFormatFlags & kAudioFormatFlagIsFloat) != 0;
  if (list->mNumberBuffers < (planar ? 2 : 1)) {
    CFRelease(block);
    free(list);
    return;
  }
  if ((floating && fmt->mBitsPerChannel != 32) ||
      (!floating && fmt->mBitsPerChannel != 16)) {
    [self.lock lock];
    self.failure = @"ScreenCaptureKit returned unsupported PCM sample type";
    [self.lock unlock];
    CFRelease(block);
    free(list);
    return;
  }
  size_t frames = CMSampleBufferGetNumSamples(sample);
  [self.lock lock];
  for (size_t f = 0; f < frames; f++) {
    // Keep at most 100ms; dropping old audio bounds latency after a slow
    // reader.
    if (count + 2 > UR_AUDIO_CAPACITY) {
      head = (head + 2) % UR_AUDIO_CAPACITY;
      count -= 2;
    }
    for (int ch = 0; ch < 2; ch++) {
      AudioBuffer b = list->mBuffers[planar ? ch : 0];
      size_t index = planar ? f : f * 2 + ch;
      int16_t v = 0;
      if ((index + 1) * (fmt->mBitsPerChannel / 8) <= b.mDataByteSize) {
        if (floating) {
          float x = ((float *)b.mData)[index];
          v = (int16_t)(fmaxf(-1, fminf(1, x)) * 32767);
        } else
          v = ((int16_t *)b.mData)[index];
      }
      samples[(head + count) % UR_AUDIO_CAPACITY] = v;
      count++;
    }
  }
  [self.lock unlock];
  CFRelease(block);
  free(list);
}
@end
void *ur_audio_open(char **err) {
  @autoreleasepool {
    if (!CGPreflightScreenCaptureAccess()) {
      CGRequestScreenCaptureAccess();
      *err = strdup("System audio capture needs Screen Recording permission: "
                    "System Settings > Privacy & Security > Screen & System "
                    "Audio Recording; enable urdp or its launching "
                    "terminal/sshd-keygen-wrapper, then restart urdp");
      return NULL;
    }
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    __block SCShareableContent *content;
    __block NSError *error;
    [SCShareableContent
        getShareableContentExcludingDesktopWindows:YES
                               onScreenWindowsOnly:YES
                                 completionHandler:^(SCShareableContent *c,
                                                     NSError *e) {
                                   content = c;
                                   error = e;
                                   dispatch_semaphore_signal(sem);
                                 }];
    if (dispatch_semaphore_wait(
            sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) ||
        !content.displays.count) {
      *err = strdup(error.localizedDescription.UTF8String
                        ?: "No display available for system audio capture");
      return NULL;
    }
    URAudio *s = [URAudio new];
    s.lock = [NSLock new];
    SCContentFilter *filter =
        [[SCContentFilter alloc] initWithDisplay:content.displays[0]
                                excludingWindows:@[]];
    SCStreamConfiguration *cfg = [SCStreamConfiguration new];
    cfg.capturesAudio = YES;
    cfg.excludesCurrentProcessAudio = YES;
    cfg.sampleRate = 48000;
    cfg.channelCount = 2;
    cfg.width = 2;
    cfg.height = 2;
    cfg.minimumFrameInterval = CMTimeMake(1, 1);
    cfg.queueDepth = 3;
    cfg.showsCursor = NO;
    s.stream = [[SCStream alloc] initWithFilter:filter
                                  configuration:cfg
                                       delegate:s];
    if (![s.stream addStreamOutput:s
                              type:SCStreamOutputTypeAudio
                sampleHandlerQueue:dispatch_queue_create("urdp.audio",
                                                         DISPATCH_QUEUE_SERIAL)
                             error:&error]) {
      *err = strdup(error.localizedDescription.UTF8String);
      return NULL;
    }
    sem = dispatch_semaphore_create(0);
    __block NSError *startError;
    [s.stream startCaptureWithCompletionHandler:^(NSError *e) {
      startError = e;
      dispatch_semaphore_signal(sem);
    }];
    if (dispatch_semaphore_wait(
            sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) ||
        startError) {
      [s.stream stopCaptureWithCompletionHandler:nil];
      *err = strdup(startError.localizedDescription.UTF8String
                        ?: "Audio capture start timed out");
      return NULL;
    }
    return (__bridge_retained void *)s;
  }
}
int ur_audio_read(void *ptr, int16_t *out, int n, char **err) {
  @autoreleasepool {
    URAudio *s = (__bridge URAudio *)ptr;
    [s.lock lock];
    if (s.failure) {
      *err = strdup(s.failure.UTF8String);
      [s.lock unlock];
      return -1;
    }
    int available = (int)MIN((size_t)n, s->count);
    for (int i = 0; i < available; i++)
      out[i] = s->samples[(s->head + i) % UR_AUDIO_CAPACITY];
    s->head = (s->head + available) % UR_AUDIO_CAPACITY;
    s->count -= available;
    memset(out + available, 0, (n - available) * sizeof(int16_t));
    [s.lock unlock];
    return 0;
  }
}
void ur_audio_close(void *ptr) {
  if (!ptr)
    return;
  @autoreleasepool {
    URAudio *s = CFBridgingRelease(ptr);
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [s.stream stopCaptureWithCompletionHandler:^(NSError *e) {
      dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem,
                            dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
    [s.stream removeStreamOutput:s type:SCStreamOutputTypeAudio error:nil];
  }
}
