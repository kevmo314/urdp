package encoder

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// vp8KeyframeSize parses a VP8 keyframe header (RFC 6386 9.1).
func vp8KeyframeSize(b []byte) (w, h int, ok bool) {
	if len(b) < 10 || b[0]&1 != 0 || !bytes.Equal(b[3:6], []byte{0x9d, 0x01, 0x2a}) {
		return 0, 0, false
	}
	return int(binary.LittleEndian.Uint16(b[6:]) & 0x3fff), int(binary.LittleEndian.Uint16(b[8:]) & 0x3fff), true
}

// vp9IsKeyframe checks a profile 0 VP9 frame header: frame marker, then
// show_existing_frame=0, frame_type=KEY_FRAME, then the sync code.
func vp9IsKeyframe(b []byte) bool {
	return len(b) > 4 && b[0]&0xf0 == 0x80 && b[0]&0x08 == 0 && b[0]&0x04 == 0 &&
		bytes.Equal(b[1:4], []byte{0x49, 0x83, 0x42})
}

func TestVPXEncode(t *testing.T) {
	const w, h = 320, 240
	for _, c := range []Codec{VP8, VP9} {
		t.Run(string(c), func(t *testing.T) {
			enc, err := newVPX(c, w, h, Options{FPS: 30})
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Close()
			frames := encodeSequence(t, enc, w, h, 20, 0, 10)
			for i, f := range frames {
				var key bool
				if c == VP8 {
					fw, fh, ok := vp8KeyframeSize(f.data)
					key = ok
					if ok && (fw != w || fh != h) {
						t.Errorf("frame %d: keyframe size %dx%d", i, fw, fh)
					}
				} else {
					key = vp9IsKeyframe(f.data)
				}
				if key != f.key {
					t.Errorf("frame %d: header says keyframe=%v, encoder says %v", i, key, f.key)
				}
			}
			fourcc := map[Codec]string{VP8: "VP80", VP9: "VP90"}[c]
			checkDecodes(t, fourcc, w, h, frames)

			if err := enc.SetBitrate(2000); err != nil {
				t.Fatal(err)
			}
			encodeSequence(t, enc, w, h, 2, 0)
		})
	}
}

func TestVPXSelected(t *testing.T) {
	for _, c := range []Codec{VP8, VP9} {
		e, err := New(c, 64, 64, Options{Backend: "software"})
		if err != nil {
			t.Fatal(err)
		}
		if e.Codec() != c {
			t.Errorf("codec %s, want %s", e.Codec(), c)
		}
		e.Close()
	}
}
