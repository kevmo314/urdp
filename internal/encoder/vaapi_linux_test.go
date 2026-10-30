package encoder

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// traceHeaders runs FFmpeg's trace_headers bitstream filter, which parses
// every header field with FFmpeg's own (CBS) parser.
func traceHeaders(t *testing.T, format string, stream []byte) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "s."+format)
	if err := os.WriteFile(path, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-f", format, "-i", path,
		"-c", "copy", "-bsf:v", "trace_headers", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	// Keep only the filter's lines: the decoder also probes the stand-in
	// slice data and (rightly) complains about it.
	var trace []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "trace_headers") {
			trace = append(trace, l)
		}
	}
	return strings.Join(trace, "\n")
}

// field returns the values FFmpeg traced for a header field, in order.
func field(trace, name string) []string {
	var vals []string
	for _, m := range regexp.MustCompile(`\s`+regexp.QuoteMeta(name)+`\s+[01]+ = (-?\d+)`).FindAllStringSubmatch(trace, -1) {
		vals = append(vals, m[1])
	}
	return vals
}

func checkField(t *testing.T, trace, name string, want ...string) {
	t.Helper()
	got := field(trace, name)
	// Parameter sets are traced again from the extradata FFmpeg extracts,
	// so a single wanted value must match every occurrence.
	if len(want) == 1 && len(got) > 1 {
		want = slices.Repeat(want, len(got))
	}
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", name, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
	}
}

// The packed headers must parse as the encoder configured them: an IDR
// then two P frames at 1080p (cropped from 1088).
func TestVAAPIPackedHeadersH264(t *testing.T) {
	var s []byte
	for i := range 3 {
		s = append(s, vaapiTestHeaders(H264, 1920, 1080, false, i == 0, i)...)
	}
	tr := traceHeaders(t, "h264", s)
	if regexp.MustCompile(`(?i)error|invalid`).MatchString(tr) {
		t.Fatalf("trace reports errors:\n%s", tr)
	}
	checkField(t, tr, "profile_idc", "66")
	checkField(t, tr, "level_idc", "42")
	checkField(t, tr, "pic_width_in_mbs_minus1", "119")
	checkField(t, tr, "pic_height_in_map_units_minus1", "67")
	checkField(t, tr, "frame_crop_bottom_offset", "4")
	checkField(t, tr, "pic_order_cnt_type", "2")
	checkField(t, tr, "max_num_reorder_frames", "0")
	checkField(t, tr, "colour_primaries", "1")
	checkField(t, tr, "entropy_coding_mode_flag", "0")
	checkField(t, tr, "slice_type", "7", "5", "5")
	checkField(t, tr, "frame_num", "0", "1", "2")
	checkField(t, tr, "idr_pic_id", "1")
	checkField(t, tr, "disable_deblocking_filter_idc", "0", "0", "0")
}

func TestVAAPIPackedHeadersHEVC(t *testing.T) {
	for _, sao := range []bool{false, true} {
		var s []byte
		for i := range 3 {
			s = append(s, vaapiTestHeaders(H265, 1920, 1080, sao, i == 0, i)...)
		}
		tr := traceHeaders(t, "hevc", s)
		if regexp.MustCompile(`(?i)error|invalid`).MatchString(tr) {
			t.Fatalf("sao=%v: trace reports errors:\n%s", sao, tr)
		}
		checkField(t, tr, "general_profile_idc", "1") // VPS and SPS
		checkField(t, tr, "general_level_idc", "123")
		checkField(t, tr, "pic_width_in_luma_samples", "1920")
		checkField(t, tr, "pic_height_in_luma_samples", "1088")
		checkField(t, tr, "conf_win_bottom_offset", "4")
		checkField(t, tr, "sps_max_num_reorder_pics[0]", "0")
		checkField(t, tr, "slice_type", "2", "1", "1")
		checkField(t, tr, "slice_pic_order_cnt_lsb", "1", "2")
		checkField(t, tr, "num_negative_pics", "1", "1")
		checkField(t, tr, "cu_qp_delta_enabled_flag", "1")
		if sao {
			checkField(t, tr, "slice_sao_luma_flag", "1", "1", "1")
		}
	}
}

// Encoding for real needs a VA-API device with an H.264/HEVC encoder;
// without one this checks that the backend fails fast with a reason.
func TestVAAPIEncode(t *testing.T) {
	const w, h = 320, 240
	for _, c := range []Codec{H264, H265} {
		t.Run(string(c), func(t *testing.T) {
			enc, err := newVAAPI(c, w, h, Options{FPS: 30})
			if err != nil {
				t.Skipf("no VA-API %s encoder: %v", c, err)
			}
			defer enc.Close()
			frames := encodeSequence(t, enc, w, h, 20, 0, 10)
			for i, f := range frames {
				types := nalTypes(f.data)
				if c == H264 && contains(types, 5) != f.key {
					t.Errorf("frame %d: NAL types %v, keyframe %v", i, types, f.key)
				}
				if c == H265 && f.key && !bytes.Contains(f.data, []byte{0, 0, 1, 0x40, 0x01}) {
					t.Errorf("frame %d: keyframe lacks a VPS", i)
				}
			}
			if err := enc.SetBitrate(2000); err != nil {
				t.Fatal(err)
			}
			encodeSequence(t, enc, w, h, 2, 0)
			// ffmpeg reads raw Annex B streams directly.
			path := filepath.Join(t.TempDir(), "out.bin")
			var all []byte
			for _, f := range frames {
				all = append(all, f.data...)
			}
			os.WriteFile(path, all, 0o644)
			format := map[Codec]string{H264: "h264", H265: "hevc"}[c]
			out, err := exec.Command("ffprobe", "-v", "error", "-f", format, "-count_frames", "-show_entries",
				"stream=width,height,nb_read_frames", "-of", "default=nw=1", path).CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("width=320\nheight=240\n")) || !bytes.Contains(out, []byte("nb_read_frames=20")) {
				t.Errorf("ffprobe: %v\n%s", err, out)
			}
		})
	}
}
