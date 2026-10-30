#import <AppKit/AppKit.h>
#import <CoreMedia/CoreMedia.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>

// Shared wakeups let a stitched desktop wait for any display without polling.
static uint64_t generation;
static NSCondition *updates(void) {
  static NSCondition *condition;
  static dispatch_once_t once;
  dispatch_once(&once, ^{
    condition = [NSCondition new];
  });
  return condition;
}
static void notifyUpdate(void) {
  NSCondition *c = updates();
  [c lock];
  generation++;
  [c broadcast];
  [c unlock];
}
uint64_t ur_generation(void) {
  NSCondition *c = updates();
  [c lock];
  uint64_t result = generation;
  [c unlock];
  return result;
}
void ur_wait(uint64_t previous, double seconds) {
  @autoreleasepool {
    NSCondition *c = updates();
    NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:MAX(0, seconds)];
    [c lock];
    while (generation == previous && seconds > 0)
      if (![c waitUntilDate:deadline])
        break;
    [c unlock];
  }
}

// ARC owns streams and delegates. Pixel buffers are retained separately while
// pending or exposed to Go; the callback never mutates an exposed buffer.
@interface URCapture : NSObject <SCStreamOutput, SCStreamDelegate>
@property SCStream *stream;
@property NSCondition *condition;
@property NSString *failure;
@property CVPixelBufferRef pending;
@property CVPixelBufferRef held;
@property CGRect bounds;
@property uint32_t windowID;
@property SCStreamConfiguration *configuration;
@property CGFloat scale;
@property BOOL closed;
@end
@implementation URCapture
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
  [self.condition lock];
  self.failure = error.localizedDescription;
  [self.condition broadcast];
  notifyUpdate();
  [self.condition unlock];
}
- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sample
                   ofType:(SCStreamOutputType)type {
  if (type != SCStreamOutputTypeScreen || !CMSampleBufferIsValid(sample))
    return;
  NSArray *attachments =
      (__bridge NSArray *)CMSampleBufferGetSampleAttachmentsArray(sample,
                                                                  false);
  NSNumber *status = attachments.firstObject[SCStreamFrameInfoStatus];
  if (!status || status.integerValue != SCFrameStatusComplete)
    return;
  CVPixelBufferRef pixel = CMSampleBufferGetImageBuffer(sample);
  if (!pixel)
    return;
  [self.condition lock];
  if (self.pending)
    CVPixelBufferRelease(self.pending);
  self.pending = self.closed ? NULL : CVPixelBufferRetain(pixel);
  [self.condition broadcast];
  notifyUpdate();
  [self.condition unlock];
}
@end

static BOOL permission(void) {
  if (CGPreflightScreenCaptureAccess())
    return YES;
  CGRequestScreenCaptureAccess();
  return NO;
}
static SCShareableContent *content(NSString **failure) {
  if (!permission()) {
    *failure = @"Screen Recording permission required: System Settings > "
               @"Privacy & Security > Screen & System Audio Recording (Screen "
               @"Recording on macOS 13); enable urdp or its launching "
               @"terminal/sshd-keygen-wrapper, then restart urdp";
    return nil;
  }
  dispatch_semaphore_t sem = dispatch_semaphore_create(0);
  __block SCShareableContent *result;
  __block NSError *error;
  [SCShareableContent
      getShareableContentExcludingDesktopWindows:YES
                             onScreenWindowsOnly:YES
                               completionHandler:^(SCShareableContent *c,
                                                   NSError *e) {
                                 result = c;
                                 error = e;
                                 dispatch_semaphore_signal(sem);
                               }];
  if (dispatch_semaphore_wait(
          sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC))) {
    *failure = @"ScreenCaptureKit enumeration timed out";
    return nil;
  }
  if (!result)
    *failure =
        error.localizedDescription ?: @"ScreenCaptureKit enumeration failed";
  return result;
}
static NSArray<SCDisplay *> *sortedDisplays(SCShareableContent *c) {
  return [c.displays sortedArrayUsingComparator:^NSComparisonResult(
                         SCDisplay *a, SCDisplay *b) {
    if (a.displayID == b.displayID)
      return NSOrderedSame;
    if (a.displayID == CGMainDisplayID())
      return NSOrderedAscending;
    if (b.displayID == CGMainDisplayID())
      return NSOrderedDescending;
    return a.displayID < b.displayID ? NSOrderedAscending : NSOrderedDescending;
  }];
}
char *ur_list(int displays, char **err) {
  @autoreleasepool {
    NSString *failure;
    SCShareableContent *c = content(&failure);
    if (!c) {
      *err = strdup(failure.UTF8String);
      return NULL;
    }
    NSMutableArray *items = [NSMutableArray new];
    if (displays) {
      int i = 0;
      for (SCDisplay *d in sortedDisplays(c)) {
        CGRect r = CGDisplayBounds(d.displayID);
        [items addObject:@{
          @"index" : @(++i),
          @"name" : [NSString stringWithFormat:@"Display %u", d.displayID],
          @"x" : @(r.origin.x),
          @"y" : @(r.origin.y),
          @"width" : @(r.size.width),
          @"height" : @(r.size.height),
          @"primary" : @(d.displayID == CGMainDisplayID())
        }];
      }
    } else
      for (SCWindow *w in c.windows) {
        if (!w.onScreen || w.windowLayer != 0 || w.frame.size.width < 1 ||
            w.frame.size.height < 1)
          continue;
        [items addObject:@{
          @"id" : @(w.windowID),
          @"title" : w.title ?: @"",
          @"app" : w.owningApplication.applicationName ?: @"",
          @"width" : @(w.frame.size.width),
          @"height" : @(w.frame.size.height)
        }];
      }
    NSData *json = [NSJSONSerialization dataWithJSONObject:items
                                                   options:0
                                                     error:nil];
    return strdup([[NSString alloc] initWithData:json
                                        encoding:NSUTF8StringEncoding]
                      .UTF8String);
  }
}
// Window output follows the native backing resolution when resized or moved
// between displays. Use the largest intersecting scale for spanning windows.
static CGFloat scaleForRect(CGRect rect) {
  uint32_t count = 0;
  if (CGGetActiveDisplayList(0, NULL, &count) != kCGErrorSuccess)
    return 1;
  CGDirectDisplayID *ids = calloc(count, sizeof(CGDirectDisplayID));
  if (!ids)
    return 1;
  CGFloat scale = 1;
  if (CGGetActiveDisplayList(count, ids, &count) == kCGErrorSuccess) {
    for (uint32_t i = 0; i < count; i++) {
      if (!CGRectIntersectsRect(CGDisplayBounds(ids[i]), rect))
        continue;
      CGDisplayModeRef mode = CGDisplayCopyDisplayMode(ids[i]);
      if (mode) {
        scale = MAX(scale, (double)CGDisplayModeGetPixelWidth(mode) /
                               CGDisplayModeGetWidth(mode));
        CGDisplayModeRelease(mode);
      }
    }
  }
  free(ids);
  return scale;
}

void *ur_open(uint32_t window, int display, char **err) {
  @autoreleasepool {
    NSString *failure;
    SCShareableContent *c = content(&failure);
    if (!c) {
      *err = strdup(failure.UTF8String);
      return NULL;
    }
    URCapture *s = [URCapture new];
    s.condition = [NSCondition new];
    s.windowID = window;
    SCContentFilter *filter;
    SCStreamConfiguration *cfg = [SCStreamConfiguration new];
    if (window) {
      SCWindow *found;
      for (SCWindow *w in c.windows)
        if (w.windowID == window) {
          found = w;
          break;
        }
      if (!found) {
        *err = strdup("window not found");
        return NULL;
      }
      filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:found];
      s.bounds = found.frame;
      CGFloat scale = scaleForRect(found.frame);
      s.scale = scale;
      cfg.width = ceil(found.frame.size.width * scale);
      cfg.height = ceil(found.frame.size.height * scale);
    } else {
      NSArray *ds = sortedDisplays(c);
      if (display < 1 || display > ds.count) {
        *err = strdup("display not found");
        return NULL;
      }
      SCDisplay *d = ds[display - 1];
      s.bounds = CGDisplayBounds(d.displayID);
      filter = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
      CGDisplayModeRef mode = CGDisplayCopyDisplayMode(d.displayID);
      cfg.width = mode ? CGDisplayModeGetPixelWidth(mode) : d.width;
      cfg.height = mode ? CGDisplayModeGetPixelHeight(mode) : d.height;
      if (mode)
        CGDisplayModeRelease(mode);
    }
    cfg.minimumFrameInterval = CMTimeMake(1, 120);
    cfg.queueDepth = 3;
    cfg.pixelFormat = kCVPixelFormatType_32BGRA;
    cfg.showsCursor = NO;
    s.configuration = cfg;
    s.stream = [[SCStream alloc] initWithFilter:filter
                                  configuration:cfg
                                       delegate:s];
    NSError *error;
    if (![s.stream addStreamOutput:s
                              type:SCStreamOutputTypeScreen
                sampleHandlerQueue:dispatch_queue_create("urdp.screen",
                                                         DISPATCH_QUEUE_SERIAL)
                             error:&error]) {
      *err = strdup(error.localizedDescription.UTF8String);
      return NULL;
    }
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
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
                        ?: "ScreenCaptureKit start timed out");
      return NULL;
    }
    return (__bridge_retained void *)s;
  }
}
// Returns -2 when a window disappears, -1 on error, 0 on timeout, 1 on change.
int ur_frame(void *ptr, double wait, void **pixels, int *width, int *height,
             int *stride, double *bounds, char **err) {
  @autoreleasepool {
    URCapture *s = (__bridge URCapture *)ptr;
    if (s.windowID) {
      NSArray *ws = CFBridgingRelease(CGWindowListCopyWindowInfo(
          kCGWindowListOptionIncludingWindow, s.windowID));
      if (!ws.count)
        return -2;
      CGRect r;
      if (CGRectMakeWithDictionaryRepresentation(
              (__bridge CFDictionaryRef)
                  ws[0][(__bridge NSString *)kCGWindowBounds],
              &r)) {
        CGFloat scale =
            CGRectEqualToRect(r, s.bounds) ? s.scale : scaleForRect(r);
        if (!CGSizeEqualToSize(r.size, s.bounds.size) || scale != s.scale) {
          s.scale = scale;
          s.configuration.width = MAX(1, ceil(r.size.width * s.scale));
          s.configuration.height = MAX(1, ceil(r.size.height * s.scale));
          [s.stream updateConfiguration:s.configuration
                      completionHandler:^(NSError *error) {
                        if (error)
                          [s stream:s.stream didStopWithError:error];
                      }];
        }
        s.bounds = r;
      }
    }
    [s.condition lock];
    NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:MAX(0, wait)];
    while (!s.pending && !s.failure && wait > 0)
      if (![s.condition waitUntilDate:deadline])
        break;
    if (s.failure) {
      *err = strdup(s.failure.UTF8String);
      [s.condition unlock];
      return -1;
    }
    BOOL changed = s.pending != NULL;
    if (changed) {
      if (s.held) {
        CVPixelBufferUnlockBaseAddress(s.held, kCVPixelBufferLock_ReadOnly);
        CVPixelBufferRelease(s.held);
      }
      s.held = s.pending;
      s.pending = NULL;
      if (CVPixelBufferLockBaseAddress(s.held, kCVPixelBufferLock_ReadOnly) !=
          kCVReturnSuccess) {
        CVPixelBufferRelease(s.held);
        s.held = NULL;
        *err = strdup("ScreenCaptureKit pixel buffer could not be locked");
        [s.condition unlock];
        return -1;
      }
    }
    if (s.held) {
      *pixels = CVPixelBufferGetBaseAddress(s.held);
      *width = (int)CVPixelBufferGetWidth(s.held);
      *height = (int)CVPixelBufferGetHeight(s.held);
      *stride = (int)CVPixelBufferGetBytesPerRow(s.held);
    }
    bounds[0] = s.bounds.origin.x;
    bounds[1] = s.bounds.origin.y;
    bounds[2] = s.bounds.size.width;
    bounds[3] = s.bounds.size.height;
    [s.condition unlock];
    return changed;
  }
}
void ur_close(void *ptr) {
  if (!ptr)
    return;
  @autoreleasepool {
    URCapture *s = CFBridgingRelease(ptr);
    [s.condition lock];
    s.closed = YES;
    [s.condition unlock];
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [s.stream stopCaptureWithCompletionHandler:^(NSError *e) {
      dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem,
                            dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
    [s.stream removeStreamOutput:s type:SCStreamOutputTypeScreen error:nil];
    [s.condition lock];
    if (s.pending) {
      CVPixelBufferRelease(s.pending);
      s.pending = NULL;
    }
    if (s.held) {
      CVPixelBufferUnlockBaseAddress(s.held, kCVPixelBufferLock_ReadOnly);
      CVPixelBufferRelease(s.held);
      s.held = NULL;
    }
    [s.condition unlock];
  }
}
int ur_icon(uint32_t window, int size, unsigned char *pixels) {
  @autoreleasepool {
    NSArray *ws = CFBridgingRelease(
        CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, window));
    if (!ws.count)
      return 0;
    pid_t pid = [ws[0][(__bridge NSString *)kCGWindowOwnerPID] intValue];
    NSRunningApplication *app =
        [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    if (!app)
      return 0;
    NSImage *icon = app.icon;
    if (!icon && app.bundleURL.path)
      icon = [[NSWorkspace sharedWorkspace] iconForFile:app.bundleURL.path];
    if (!icon)
      return 0;
    CGImageRef im = [icon CGImageForProposedRect:NULL context:nil hints:nil];
    if (!im)
      return 0;
    CGColorSpaceRef cs = CGColorSpaceCreateDeviceRGB();
    CGContextRef ctx = CGBitmapContextCreate(
        pixels, size, size, 8, size * 4, cs,
        kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
    CGColorSpaceRelease(cs);
    if (!ctx)
      return 0;
    CGContextDrawImage(ctx, CGRectMake(0, 0, size, size), im);
    CGContextRelease(ctx);
    return 1;
  }
}
