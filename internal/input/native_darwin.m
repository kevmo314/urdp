#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
int ur_trusted(void) {
  @autoreleasepool {
    return AXIsProcessTrustedWithOptions(
        (__bridge CFDictionaryRef)
            @{(__bridge NSString *)kAXTrustedCheckOptionPrompt : @YES});
  }
}
void ur_mouse(int x, int y, int button, int down, int moving, int held) {
  CGEventRef current = CGEventCreate(NULL);
  CGPoint point = moving ? CGPointMake(x, y) : CGEventGetLocation(current);
  CFRelease(current);
  CGMouseButton b = kCGMouseButtonLeft;
  CGEventType type = kCGEventMouseMoved;
  int selected = moving ? held : button;
  switch (selected) {
  case 0:
    b = kCGMouseButtonLeft;
    type = moving ? kCGEventLeftMouseDragged
                  : (down ? kCGEventLeftMouseDown : kCGEventLeftMouseUp);
    break;
  case 1:
    b = kCGMouseButtonCenter;
    type = moving ? kCGEventOtherMouseDragged
                  : (down ? kCGEventOtherMouseDown : kCGEventOtherMouseUp);
    break;
  case 2:
    b = kCGMouseButtonRight;
    type = moving ? kCGEventRightMouseDragged
                  : (down ? kCGEventRightMouseDown : kCGEventRightMouseUp);
    break;
  }
  CGEventRef e = CGEventCreateMouseEvent(NULL, type, point, b);
  CGEventPost(kCGHIDEventTap, e);
  CFRelease(e);
}
void ur_wheel(int dx, int dy) {
  CGEventRef e =
      CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 2, -dy, -dx);
  CGEventPost(kCGHIDEventTap, e);
  CFRelease(e);
}
void ur_key(int key, int down, uint64_t flags, int repeat) {
  CGEventRef e = CGEventCreateKeyboardEvent(NULL, key, down);
  CGEventSetFlags(e, flags);
  CGEventSetIntegerValueField(e, kCGKeyboardEventAutorepeat, repeat);
  CGEventPost(kCGHIDEventTap, e);
  CFRelease(e);
}
int ur_focus(uint32_t window) {
  @autoreleasepool {
    NSArray *ws = CFBridgingRelease(
        CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, window));
    if (!ws.count)
      return 0;
    NSDictionary *info = ws[0];
    pid_t pid = [info[(__bridge NSString *)kCGWindowOwnerPID] intValue];
    NSRunningApplication *app =
        [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    [app activateWithOptions:NSApplicationActivateIgnoringOtherApps];
    // AX has no public window-ID lookup. Match the selected CG window's bounds
    // and title against the owner's AX windows, then raise the matching window.
    AXUIElementRef ax = AXUIElementCreateApplication(pid);
    CFTypeRef value = NULL;
    if (AXUIElementCopyAttributeValue(ax, kAXWindowsAttribute, &value) ==
        kAXErrorSuccess) {
      CGRect rect;
      CGRectMakeWithDictionaryRepresentation(
          (__bridge CFDictionaryRef)info[(__bridge NSString *)kCGWindowBounds],
          &rect);
      for (id item in (__bridge NSArray *)value) {
        AXUIElementRef w = (__bridge AXUIElementRef)item;
        CFTypeRef p = NULL, t = NULL;
        AXUIElementCopyAttributeValue(w, kAXPositionAttribute, &p);
        AXUIElementCopyAttributeValue(w, kAXTitleAttribute, &t);
        CGPoint pos;
        BOOL match = p && AXValueGetValue(p, kAXValueCGPointType, &pos) &&
                     fabs(pos.x - rect.origin.x) < 2 &&
                     fabs(pos.y - rect.origin.y) < 2;
        NSString *title = info[(__bridge NSString *)kCGWindowName];
        if (title.length && t)
          match = match && [title isEqual:(__bridge NSString *)t];
        if (p)
          CFRelease(p);
        if (t)
          CFRelease(t);
        if (match) {
          AXUIElementPerformAction(w, kAXRaiseAction);
          AXUIElementSetAttributeValue(ax, kAXFocusedWindowAttribute, w);
          break;
        }
      }
      CFRelease(value);
    }
    CFRelease(ax);
    return app != nil;
  }
}
