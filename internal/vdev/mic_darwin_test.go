package vdev

import (
	"errors"
	"io/fs"
	"math"
	"strings"
	"testing"
	"time"
)

func TestOSStatus(t *testing.T) {
	if s := osStatus(0x21646576).Error(); s != "OSStatus '!dev'" {
		t.Errorf("got %s", s)
	}
	if s := osStatus(-50).Error(); s != "OSStatus -50" {
		t.Errorf("got %s", s)
	}
}

func TestEmbeddedDriver(t *testing.T) {
	plist, err := fs.ReadFile(driverFS, driverRoot+"/Contents/Info.plist")
	if err != nil {
		t.Skip("driver not built (make mac-driver)")
	}
	for _, want := range []string{"urdp_audio_create", "443ABAB8-E7B3-491A-B985-BEB9187030DB"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("Info.plist lacks %s", want)
		}
	}
	if _, err := fs.Stat(driverFS, driverRoot+"/Contents/_CodeSignature/CodeResources"); err != nil {
		t.Errorf("driver isn't signed: %v", err)
	}
}

// TestMicrophone plays a tone through the virtual microphone and records
// it back from the device's input side, as an app would.
func TestMicrophone(t *testing.T) {
	mic, err := OpenMicrophone()
	if errors.Is(err, ErrUnsupported) {
		if !strings.Contains(err.Error(), "-install-audio-driver") {
			t.Errorf("unhelpful error: %v", err)
		}
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer mic.Close()

	rec, err := recordMicrophone(micRate * 3)
	if err != nil {
		t.Fatal(err)
	}
	recStart := time.Now()
	time.Sleep(300 * time.Millisecond)

	const freq, amp, chunk = 1000.0, 0.5, micRate / 100
	pcm := make([]int16, chunk*2)
	toneStart := time.Now()
	tick := time.NewTicker(10 * time.Millisecond)
	for i := 0; i < 100; i++ {
		for j := 0; j < chunk; j++ {
			n := i*chunk + j
			v := int16(amp * 32767 * math.Sin(2*math.Pi*freq*float64(n)/micRate))
			pcm[2*j], pcm[2*j+1] = v, v
		}
		if err := mic.Write(pcm); err != nil {
			t.Fatal(err)
		}
		<-tick.C
	}
	tick.Stop()
	time.Sleep(300 * time.Millisecond)
	got := rec.stop()
	st := mic.(*coreAudioMic).stats()
	t.Logf("recorded %d frames; mic %+v", len(got), st)

	onset := -1
	for i, s := range got {
		if math.Abs(float64(s)) > 0.05 {
			onset = i
			break
		}
	}
	if onset < 0 {
		if !micAuthorized() {
			// Over SSH, the responsible process is sshd, which can't
			// be granted the permission.
			t.Skip("recorded silence: this process lacks the Microphone permission " +
				"(run from Terminal and allow it in System Settings > Privacy & Security > Microphone)")
		}
		t.Fatal("recorded only silence")
	}
	expected := int(toneStart.Sub(recStart).Seconds() * micRate)
	t.Logf("tone heard %.1fms after it was written", float64(onset-expected)*1000/micRate)

	// The middle of the tone: right level and pitch.
	win := got[onset+micRate/10 : onset+micRate/10+micRate/2]
	var sum float64
	crossings := 0
	for i, s := range win {
		sum += float64(s) * float64(s)
		if i > 0 && (win[i-1] < 0) != (s < 0) {
			crossings++
		}
	}
	rms := math.Sqrt(sum / float64(len(win)))
	hz := float64(crossings) / 2 / (float64(len(win)) / micRate)
	if math.Abs(rms-amp/math.Sqrt2) > 0.05 || math.Abs(hz-freq) > 20 {
		t.Errorf("tone at %.0fHz rms %.3f, want %.0fHz rms %.3f", hz, rms, freq, amp/math.Sqrt2)
	}
	// Once urdp stops writing the input goes quiet rather than looping
	// stale audio.
	for _, s := range got[len(got)-micRate/10:] {
		if s != 0 {
			t.Fatalf("input not silent after the tone: %v", s)
		}
	}
}
