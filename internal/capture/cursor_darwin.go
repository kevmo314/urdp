package capture

/*
#include <stdlib.h>
int ur_cursor(char **, void **, int *, int *, int *, int *);
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"
	"unsafe"
)

// WatchCursor reports the initial system cursor and subsequent image, hotspot
// or visibility changes until ctx is canceled.
func WatchCursor(ctx context.Context, fn func(Cursor)) error {
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	var previous Cursor
	first := true
	for {
		var css *C.char
		var pixels unsafe.Pointer
		var length, x, y, hidden C.int
		if C.ur_cursor(&css, &pixels, &length, &x, &y, &hidden) != 0 {
			data := C.GoBytes(pixels, length)
			C.free(pixels)
			// Include the hotspot in the identity, even if two images are identical.
			hash := sha256.New()
			hash.Write(data)
			_ = binary.Write(hash, binary.LittleEndian, [2]int32{int32(x), int32(y)})
			c := Cursor{Serial: binary.LittleEndian.Uint64(hash.Sum(nil)), CSS: C.GoString(css), HotX: int(x), HotY: int(y), Hidden: hidden != 0}
			if c.CSS == "" {
				c.PNG = data
			}
			if first || c.Serial != previous.Serial || c.Hidden != previous.Hidden {
				fn(c)
				previous = c
				first = false
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
