package server

import (
	"testing"
	"time"

	"github.com/kevmo314/urdp/internal/capture"
)

func TestResScaler(t *testing.T) {
	var r resScaler
	now := time.Unix(0, 0)
	step := func(d time.Duration, bps int) int {
		now = now.Add(d)
		return r.update(now, 3440, 1440, bps)
	}
	// 20 Mbps is plenty for 3440x1440.
	if s := step(0, 20e6); s != 1 {
		t.Fatalf("scale %d at 20 Mbps", s)
	}
	// 2 Mbps (0.013 bpp) shrinks after 2s, not before.
	if s := step(time.Second, 2e6); s != 1 {
		t.Fatalf("shrank immediately: %d", s)
	}
	if s := step(2*time.Second, 2e6); s != 2 {
		t.Fatalf("scale %d after 2s at 2 Mbps", s)
	}
	// 2 Mbps at 1720x720 is 0.054 bpp: fine, and not enough to grow back
	// (full size would get 0.013).
	if s := step(30*time.Second, 2e6); s != 2 {
		t.Fatalf("scale %d at 2 Mbps after settling", s)
	}
	// 400 kbps: down to 1/16.
	step(time.Second, 4e5)
	if s := step(3*time.Second, 4e5); s != 4 {
		t.Fatalf("scale %d at 400 kbps", s)
	}
	// A brief recovery doesn't grow; a sustained one does, one step at a time.
	if s := step(5*time.Second, 20e6); s != 4 {
		t.Fatalf("grew after 5s: %d", s)
	}
	if s := step(10*time.Second, 20e6); s != 2 {
		t.Fatalf("scale %d after 10s of headroom", s)
	}
	step(time.Second, 20e6)
	if s := step(11*time.Second, 20e6); s != 1 {
		t.Fatalf("scale %d after recovering", s)
	}
	// Small sources aren't shrunk below 320 pixels across.
	var small resScaler
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		if s := small.update(now, 800, 600, 1e4); s > 2 {
			t.Fatalf("800px wide shrank by %d", s)
		}
	}
}

// TestPipelineScalesDown streams a 1280x720 source with only 100 kbps
// allowed: the encoder should drop to 640x360, then 320x180.
func TestPipelineScalesDown(t *testing.T) {
	if testing.Short() {
		t.Skip("takes several seconds")
	}
	s, ts := newTestServer(t, &fakeInjector{}, func(c *Config) {
		c.Encoder.BitrateKbps = 100
		c.Encoder.Backend = "software"
		c.OpenSource = func(string) (capture.Source, error) { return capture.NewTestSource(1280, 720), nil }
	})
	cursorClient(t, ts.URL)
	seen := map[int]bool{}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !seen[320] {
		s.mu.Lock()
		for _, p := range s.pipelines {
			if st := p.Stats(); st.Width > 0 {
				seen[st.Width] = true
			}
		}
		s.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	for _, w := range []int{1280, 640, 320} {
		if !seen[w] {
			t.Errorf("never encoded at width %d (saw %v)", w, seen)
		}
	}
}
