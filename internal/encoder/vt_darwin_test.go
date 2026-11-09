package encoder

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

func vtNALTypes(data []byte, codec Codec) []int {
	var types []int
	for _, nal := range bytes.Split(data, []byte{0, 0, 0, 1})[1:] {
		if len(nal) == 0 {
			continue
		}
		typ := int(nal[0] & 31)
		if codec == H265 {
			typ = int(nal[0]>>1) & 63
		}
		types = append(types, typ)
	}
	return types
}

func vtAvailable(t *testing.T, codec Codec) {
	t.Helper()
	for _, b := range Backends(codec) {
		if b.Name == "vt-h264" || b.Name == "vt-hevc" {
			return
		}
	}
	t.Skip("VideoToolbox hardware encoder unavailable")
}

// vtCode is the gray level frame i carries in its top band, so decoded
// frames can be matched to the inputs they came from.
func vtCode(i int) byte { return byte(40 + (i*37)%180) }

// vtFrame draws frame i: a gray band identifying it, and noise below so
// every frame costs bits and rate control has something to limit.
func vtFrame(f *capture.Frame, i int) {
	code := vtCode(i)
	rng := rand.New(rand.NewPCG(uint64(i), 1))
	for y := 0; y < f.Height; y++ {
		row := f.Pix[y*f.Stride : y*f.Stride+f.Width*4]
		if y < 128 {
			for x := 0; x < len(row); x += 4 {
				row[x], row[x+1], row[x+2], row[x+3] = code, code, code, 255
			}
			continue
		}
		// Coarse gray noise, 2x1 pixels at moderate amplitude.
		for x := 0; x < f.Width; x += 2 {
			v := byte(rng.Uint32())>>2 + 96
			for k := x; k < min(x+2, f.Width); k++ {
				row[k*4], row[k*4+1], row[k*4+2], row[k*4+3] = v, v, v, 255
			}
		}
	}
}

func TestVideoToolboxLowLatencyProperties(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(string(codec), func(t *testing.T) {
			vtAvailable(t, codec)
			e, err := newVT(codec, 1920, 1080, Options{FPS: 60, BitrateKbps: 8000})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			want := map[string]int64{"Hardware": 1, "RealTime": 1, "AllowFrameReordering": 0, "MaxFrameDelayCount": 0, "PrioritizeEncodingSpeedOverQuality": 1, "AverageBitRate": 8_000_000}
			for _, name := range []string{"Hardware", "RealTime", "AllowFrameReordering", "MaxFrameDelayCount", "PrioritizeEncodingSpeedOverQuality", "AverageBitRate"} {
				v, err := e.property(name)
				switch {
				case err != nil && (name == "MaxFrameDelayCount" || name == "PrioritizeEncodingSpeedOverQuality"):
					t.Logf("%s: not reported (%v)", name, err)
				case err != nil:
					t.Errorf("%s: %v", name, err)
				case v != want[name]:
					t.Errorf("%s = %d, want %d", name, v, want[name])
				default:
					t.Logf("%s = %d", name, v)
				}
			}
			if err := e.SetBitrate(2000); err != nil {
				t.Fatal(err)
			}
			if v, _ := e.property("AverageBitRate"); v != 2_000_000 {
				t.Errorf("AverageBitRate after SetBitrate = %d", v)
			}
		})
	}
}

// TestVideoToolbox encodes synthetic 1080p frames from CPU memory and from
// IOSurface pixel buffers (as ScreenCaptureKit delivers them), checking one
// output per input carrying that input's picture, keyframes exactly when
// asked, a live bitrate change, and that ffmpeg decodes the stream.
func TestVideoToolbox(t *testing.T) {
	const (
		w, h     = 1920, 1080
		fps      = 60
		n        = 120
		highKbps = 20000
		lowKbps  = 2000
		switchAt = 60
	)
	forced := map[int]bool{0: true, 30: true, switchAt: true, 90: true}
	for _, codec := range []Codec{H264, H265} {
		t.Run(string(codec), func(t *testing.T) {
			vtAvailable(t, codec)
			for _, gpu := range []bool{false, true} {
				name := "BGRA"
				if gpu {
					name = "IOSurface"
				}
				t.Run(name, func(t *testing.T) {
					e, err := newVT(codec, w, h, Options{FPS: fps, BitrateKbps: highKbps, KeyintSeconds: 3600})
					if err != nil {
						t.Fatal(err)
					}
					defer e.Close()
					f := &capture.Frame{Pix: make([]byte, w*h*4), Stride: w * 4, Width: w, Height: h}
					// IOSurface input: two buffers in rotation, like SCK's pool.
					var surfaces [2]unsafe.Pointer
					nv12 := NewNV12(w, h)
					if gpu {
						for i := range surfaces {
							if surfaces[i] = newPixelBuffer(w, h); surfaces[i] == nil {
								t.Fatal("pixel buffer allocation failed")
							}
							defer releasePixelBuffer(surfaces[i])
						}
					}
					var stream []byte
					var pLat, kLat []time.Duration
					var sizes, encoded, dropped []int
					carry := false // a forced keyframe the encoder dropped
					for i := 0; i < n; i++ {
						vtFrame(f, i)
						in := f
						if gpu {
							nv12.Convert(f)
							px := surfaces[i%2]
							if err := uploadNV12(px, nv12); err != nil {
								t.Fatal(err)
							}
							in = &capture.Frame{Width: w, Height: h, GPU: capture.DarwinPixelBuffer{Pointer: px}}
						}
						if i == switchAt {
							if err = e.SetBitrate(lowKbps); err != nil {
								t.Fatal(err)
							}
						}
						start := time.Now()
						data, key, err := e.Encode(in, int64(i)*1000/fps, forced[i])
						d := time.Since(start)
						if err != nil {
							t.Fatalf("frame %d: %v", i, err)
						}
						wantKey := forced[i] || carry
						if len(data) == 0 {
							// Rate control may drop a frame (reported by VT,
							// not held back: the next frame comes out alone).
							dropped = append(dropped, i)
							sizes = append(sizes, 0)
							carry = wantKey
							continue
						}
						carry = false
						if key != wantKey {
							t.Errorf("frame %d: keyframe=%v, want %v", i, key, wantKey)
						}
						if !bytes.HasPrefix(data, []byte{0, 0, 0, 1}) {
							t.Fatal("output is not Annex B")
						}
						types := vtNALTypes(data, codec)
						if key {
							kLat = append(kLat, d)
							want := []int{7, 8}
							if codec == H265 {
								want = []int{32, 33, 34}
							}
							if len(types) < len(want) || !slices.Equal(types[:len(want)], want) {
								t.Fatalf("keyframe NAL types %v, want parameter sets %v first", types, want)
							}
							if i == 0 {
								t.Logf("keyframe NAL types: %v", types)
							}
						} else {
							pLat = append(pLat, d)
						}
						sizes = append(sizes, len(data))
						encoded = append(encoded, i)
						stream = append(stream, data...)
					}
					t.Logf("%d frames in, %d out", n, len(encoded))
					// Squeezing noise into 2 Mbit/s may cost the odd frame; an
					// occasional drop is fine, a stutter or any drop at the
					// comfortable rate is not.
					if len(dropped) > 0 {
						t.Logf("rate control dropped frames %v", dropped)
					}
					if len(dropped) > 5 || (len(dropped) > 0 && dropped[0] < switchAt) {
						t.Errorf("too many drops: %v", dropped)
					}
					slices.Sort(pLat)
					slices.Sort(kLat)
					t.Logf("1080p encode latency (Encode call, input to Annex B): P frames p50=%v p95=%v max=%v; keyframes p50=%v max=%v",
						pLat[len(pLat)/2], pLat[len(pLat)*95/100], pLat[len(pLat)-1], kLat[len(kLat)/2], kLat[len(kLat)-1])
					avg := func(from, to int) float64 {
						sum, c := 0, 0
						for i := from; i < to; i++ {
							if !forced[i] {
								sum += sizes[i]
								c++
							}
						}
						return float64(sum) / float64(c)
					}
					// Skip a few frames after each change for rate control to settle.
					high, low := avg(5, switchAt), avg(switchAt+5, n)
					t.Logf("P-frame size at %d kbps: %.0f B (%.0f kbps at %d fps); after SetBitrate(%d): %.0f B (%.0f kbps)",
						highKbps, high, high*8*fps/1000, fps, lowKbps, low, low*8*fps/1000)
					if low > high/3 {
						t.Errorf("SetBitrate(%d) had little effect: %.0f B/frame before, %.0f after", lowKbps, high, low)
					}
					if kbps := low * 8 * fps / 1000; kbps > lowKbps*1.5 {
						t.Errorf("rate after SetBitrate is %.0f kbps, ceiling %d", kbps, lowKbps)
					}
					vtDecode(t, codec, stream, encoded)
				})
			}
		})
	}
}

// vtDecode decodes the stream with ffmpeg, if installed, and checks that it
// holds the given input frames in order with BT.709 signaling and no B-frames.
func vtDecode(t *testing.T, codec Codec, stream []byte, frames []int) {
	n := len(frames)
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Log("ffmpeg unavailable; decode verification skipped")
		return
	}
	path := filepath.Join(t.TempDir(), "stream.h264")
	if codec == H265 {
		path = filepath.Join(t.TempDir(), "stream.hevc")
	}
	if err = os.WriteFile(path, stream, 0600); err != nil {
		t.Fatal(err)
	}
	// A 32x32 patch of each frame's identifying band, as 8-bit luma.
	out, err := exec.Command(ffmpeg, "-v", "error", "-xerror", "-i", path, "-vf", "crop=32:32:64:32", "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	const patch = 32 * 32
	if len(out) != n*patch {
		t.Fatalf("decoded %d frames, want %d", len(out)/patch, n)
	}
	for j, i := range frames {
		sum := 0
		for _, v := range out[j*patch : (j+1)*patch] {
			sum += int(v)
		}
		got := sum / patch
		c := int(vtCode(i))
		want := c // ffmpeg expands limited-range luma back to full-range gray
		if got < want-8 || got > want+8 {
			t.Fatalf("decoded frame %d has luma %d, want %d: output doesn't match its input frame", i, got, want)
		}
	}
	t.Logf("ffmpeg decoded all %d frames, each matching its input", n)
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries",
			"stream=profile,has_b_frames,color_range,color_space,color_transfer,color_primaries", "-of", "default=nw=1", path).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		s := string(out)
		t.Logf("ffprobe: %s", strings.Join(strings.Fields(s), " "))
		for _, want := range []string{"has_b_frames=0", "color_range=tv", "color_space=bt709", "color_transfer=bt709", "color_primaries=bt709"} {
			if !strings.Contains(s, want) {
				t.Errorf("stream lacks %s", want)
			}
		}
	}
}

// Periodic keyframes without being asked, at the configured interval.
func TestVideoToolboxKeyframeInterval(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(string(codec), func(t *testing.T) {
			vtAvailable(t, codec)
			const fps = 10
			e, err := newVT(codec, 320, 240, Options{FPS: fps, KeyintSeconds: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			f := &capture.Frame{Pix: make([]byte, 320*240*4), Stride: 320 * 4, Width: 320, Height: 240}
			var keys []int
			for i := 0; i < 5*fps; i++ {
				vtFrame(f, i)
				data, key, err := e.Encode(f, int64(i)*1000/fps, false)
				if err != nil || len(data) == 0 {
					t.Fatalf("frame %d: %d bytes, %v", i, len(data), err)
				}
				if key {
					keys = append(keys, i)
				}
			}
			t.Logf("keyframes at %v", keys)
			if len(keys) == 0 || keys[0] != 0 {
				t.Fatal("first frame is not a keyframe")
			}
			for j := 1; j < len(keys); j++ {
				if keys[j]-keys[j-1] > fps {
					t.Errorf("keyframe gap %d frames, interval %d", keys[j]-keys[j-1], fps)
				}
			}
			if len(keys) < 5 {
				t.Errorf("only %d keyframes in 5 s at a 1 s interval", len(keys))
			}
		})
	}
}

func TestVideoToolboxRejectsMismatchedPixelBuffer(t *testing.T) {
	vtAvailable(t, H264)
	e, err := newVT(H264, 640, 480, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	px := newPixelBuffer(320, 240)
	defer releasePixelBuffer(px)
	if _, _, err := e.Encode(&capture.Frame{Width: 640, Height: 480, GPU: capture.DarwinPixelBuffer{Pointer: px}}, 0, true); err == nil {
		t.Fatal("a 320x240 pixel buffer was accepted by a 640x480 encoder")
	}
	// The session survives a rejected frame.
	f := &capture.Frame{Pix: make([]byte, 640*480*4), Stride: 640 * 4, Width: 640, Height: 480}
	if data, key, err := e.Encode(f, 1, true); err != nil || len(data) == 0 || !key {
		t.Fatalf("encode after rejection: %d bytes key=%v %v", len(data), key, err)
	}
}
