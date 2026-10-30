#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include <string.h>

static NSData *cursorData(NSCursor *cursor) {
  NSBitmapImageRep *rep =
      [[NSBitmapImageRep alloc] initWithData:cursor.image.TIFFRepresentation];
  return [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
}

int ur_cursor(char **css, void **png, int *length, int *x, int *y,
              int *hidden) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    // currentCursor only describes this process; currentSystemCursor sees
    // cursors set by other applications, including non-AppKit clients.
    NSCursor *cursor = NSCursor.currentSystemCursor;
    if (!cursor)
      return 0;
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    *hidden = !CGCursorIsVisible(); // Best effort on modern WindowServer.
#pragma clang diagnostic pop
    NSData *data = cursorData(cursor);
    if (!data)
      return 0;
    *length = (int)data.length;
    *png = malloc(data.length);
    if (!*png)
      return 0;
    memcpy(*png, data.bytes, data.length);
    NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithData:data];
    *x = lround(cursor.hotSpot.x * rep.pixelsWide / cursor.image.size.width);
    *y = lround(cursor.hotSpot.y * rep.pixelsHigh / cursor.image.size.height);
    // Standard shapes are immutable; encode each reference just once.
    static NSData *knownData[16];
    static NSPoint knownHotspot[16];
    static dispatch_once_t once;
    dispatch_once(&once, ^{
      NSCursor *known[] = {NSCursor.arrowCursor,
                           NSCursor.IBeamCursor,
                           NSCursor.pointingHandCursor,
                           NSCursor.resizeLeftRightCursor,
                           NSCursor.resizeUpDownCursor,
                           NSCursor.crosshairCursor,
                           NSCursor.closedHandCursor,
                           NSCursor.openHandCursor,
                           NSCursor.operationNotAllowedCursor,
                           NSCursor.IBeamCursorForVerticalLayout,
                           NSCursor.dragCopyCursor,
                           NSCursor.dragLinkCursor,
                           NSCursor.resizeLeftCursor,
                           NSCursor.resizeRightCursor,
                           NSCursor.resizeUpCursor,
                           NSCursor.resizeDownCursor};
      for (NSUInteger i = 0; i < sizeof(known) / sizeof(known[0]); i++) {
        if (known[i]) {
          knownData[i] = cursorData(known[i]);
          knownHotspot[i] = known[i].hotSpot;
        }
      }
    });
    const char *names[] = {
        "default",     "text",          "pointer",  "ew-resize",
        "ns-resize",   "crosshair",     "grabbing", "grab",
        "not-allowed", "vertical-text", "copy",     "alias",
        "w-resize",    "e-resize",      "n-resize", "s-resize"};
    *css = NULL;
    for (NSUInteger i = 0; i < sizeof(knownData) / sizeof(knownData[0]); i++) {
      if (knownData[i] && NSEqualPoints(cursor.hotSpot, knownHotspot[i]) &&
          [data isEqualToData:knownData[i]]) {
        *css = (char *)names[i];
        break;
      }
    }
    return 1;
  }
}
