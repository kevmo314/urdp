package vdev

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requirePulse(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pactl"); err != nil {
		t.Skip("pactl not installed")
	}
	if _, err := exec.LookPath("parecord"); err != nil {
		t.Skip("parecord not installed")
	}
	if err := exec.Command("pactl", "info").Run(); err != nil {
		t.Skip("no PulseAudio server reachable")
	}
}

func pactlList(t *testing.T, what string) string {
	t.Helper()
	out, err := exec.Command("pactl", "list", "short", what).Output()
	if err != nil {
		t.Fatalf("pactl list %s: %v", what, err)
	}
	return string(out)
}

func micModulesLoaded(t *testing.T) int {
	n := 0
	for _, l := range strings.Split(pactlList(t, "modules"), "\n") {
		if strings.Contains(l, "sink_name="+micSink+" ") || strings.Contains(l, "source_name="+micSource+" ") {
			n++
		}
	}
	return n
}

func TestMicrophone(t *testing.T) {
	requirePulse(t)
	if n := micModulesLoaded(t); n != 0 {
		t.Fatalf("%d urdp modules already loaded", n)
	}

	m, err := OpenMicrophone()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	// A second viewer shares the same modules.
	m2, err := OpenMicrophone()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m2.Close() })
	if n := micModulesLoaded(t); n != 2 {
		t.Fatalf("got %d urdp modules, want 2", n)
	}
	if !strings.Contains(pactlList(t, "sources"), micSource+"\t") {
		t.Fatalf("source %s missing:\n%s", micSource, pactlList(t, "sources"))
	}
	if err := m2.Close(); err != nil {
		t.Fatal(err)
	}
	if n := micModulesLoaded(t); n != 2 {
		t.Fatalf("modules unloaded while still in use: %d left", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		const chunk = micRate / 100
		buf := make([]int16, chunk*micChannels)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for n := 0; ; {
			for i := 0; i < chunk; i++ {
				v := int16(8000 * math.Sin(2*math.Pi*1000*float64(n)/micRate))
				buf[2*i], buf[2*i+1] = v, v
				n++
			}
			if err := m.Write(buf); err != nil {
				t.Error(err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()

	rctx, rcancel := context.WithTimeout(context.Background(), 3000*time.Millisecond)
	defer rcancel()
	out := filepath.Join(t.TempDir(), "rec.raw")
	cmd := exec.CommandContext(rctx, "parecord", "--device="+micSource, "--raw",
		"--format=s16le", "--rate=48000", "--channels=2", "--latency-msec=20", out)
	cmd.Stderr = os.Stderr
	// SIGINT lets parecord flush what it has buffered.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	// The null sink takes a second or so to drop to a low latency after
	// it is created, so record long enough to hear the tone after that.
	cmd.Run()
	cancel()
	<-done

	rec, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, len(rec)/2)
	binary.Read(bytes.NewReader(rec[:len(samples)*2]), binary.LittleEndian, samples)
	if len(samples) < micRate/2 {
		t.Fatalf("recorded only %d samples", len(samples))
	}
	tail := samples[len(samples)/2:]
	var sum float64
	for _, s := range tail {
		sum += float64(s) * float64(s)
	}
	rms := math.Sqrt(sum / float64(len(tail)))
	t.Logf("recorded %d samples, rms %.0f", len(samples), rms)
	if rms < 1000 {
		t.Fatalf("rms %.0f: tone not heard on %s", rms, micSource)
	}

	// A burst far ahead of real time is dropped rather than queued.
	burst := make([]int16, micRate/50*micChannels)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := m.Write(burst); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("2s burst took %v to write; audio is queuing", d)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if n := micModulesLoaded(t); n != 0 {
		t.Fatalf("%d urdp modules left after close", n)
	}
}
