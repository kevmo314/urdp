package server

import "testing"

func TestRateControl(t *testing.T) {
	rc := newRateControl(1e6, 20e6)
	steps := []struct {
		target, sent int
		overuse      bool
		loss         float64
		want         int
	}{
		// Idle: GCC's estimate collapses with the send rate; ignore it.
		{target: 300e3, sent: 200e3, want: 20e6},
		// Congestion clamps to the estimate.
		{target: 3e6, sent: 4e6, overuse: true, want: 3e6},
		// Saturating the cap: follow GCC both down and up.
		{target: 2.5e6, sent: 2.9e6, want: 2.5e6},
		{target: 3.5e6, sent: 2.4e6, want: 3.5e6},
		// Idle again: hold, don't follow the estimate down.
		{target: 1.2e6, sent: 100e3, want: 3.5e6},
		// Loss clamps, but never below min.
		{target: 100e3, sent: 100e3, loss: 0.2, want: 1e6},
		// Never above max.
		{target: 50e6, sent: 1e6, want: 20e6},
	}
	for i, s := range steps {
		if got := rc.update(s.target, s.sent, s.overuse, s.loss); got != s.want {
			t.Fatalf("step %d: cap = %d, want %d", i, got, s.want)
		}
	}
}
