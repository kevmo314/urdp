package encoder

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kevmo314/urdp/internal/capture"
)

type encodedFrame struct {
	data []byte
	key  bool
}

// encodeSequence encodes n frames of moving test content, forcing keyframes
// at the given indices (a new encoder must be given 0), and checks the
// encoder is one-in-one-out.
func encodeSequence(t testing.TB, enc Encoder, w, h, n int, force ...int) []encodedFrame {
	t.Helper()
	src := capture.NewTestSource(w, h)
	var out []encodedFrame
	for i := range n {
		f, _, _ := src.Capture(0)
		forced := false
		for _, k := range force {
			forced = forced || k == i
		}
		data, key, err := enc.Encode(f, int64(i*16), forced)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(data) == 0 {
			t.Fatalf("frame %d: no output; the encoder must not delay frames", i)
		}
		if want := forced; key != want {
			t.Errorf("frame %d: keyframe=%v, want %v", i, key, want)
		}
		out = append(out, encodedFrame{append([]byte(nil), data...), key})
	}
	return out
}

// writeIVF stores frames in an IVF file, which ffmpeg reads for VP8, VP9
// and AV1 alike.
func writeIVF(t testing.TB, fourcc string, w, h int, frames []encodedFrame) string {
	t.Helper()
	hdr := make([]byte, 32)
	copy(hdr, "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:], 32)
	copy(hdr[8:], fourcc)
	binary.LittleEndian.PutUint16(hdr[12:], uint16(w))
	binary.LittleEndian.PutUint16(hdr[14:], uint16(h))
	binary.LittleEndian.PutUint32(hdr[16:], 60)
	binary.LittleEndian.PutUint32(hdr[20:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], uint32(len(frames)))
	buf := hdr
	for i, f := range frames {
		fh := make([]byte, 12)
		binary.LittleEndian.PutUint32(fh, uint32(len(f.data)))
		binary.LittleEndian.PutUint64(fh[4:], uint64(i))
		buf = append(append(buf, fh...), f.data...)
	}
	path := filepath.Join(t.TempDir(), "out.ivf")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// checkDecodes decodes the stream with ffmpeg and checks every frame
// decodes cleanly at the expected size. Skipped without ffmpeg.
func checkDecodes(t *testing.T, fourcc string, w, h int, frames []encodedFrame) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	path := writeIVF(t, fourcc, w, h, frames)
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "default=nw=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		"width=" + strconv.Itoa(w), "height=" + strconv.Itoa(h), "nb_read_frames=" + strconv.Itoa(len(frames)),
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("ffprobe output lacks %q:\n%s", want, got)
		}
	}
	// Decode errors (corrupt frames, missing references) are logged at
	// error level even when the frame count works out.
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.Contains(line, "=") {
			t.Errorf("decoder: %s", line)
		}
	}
}
