package server

import "time"

// resScaler picks a resolution divisor (1, 2 or 4: full, 1/4 or 1/16 of
// the pixels) from the bitrate the network allows. Below about 0.02 bits
// per pixel (at 30 fps) screen content falls apart, so a smaller picture
// that the viewer scales up looks better than a full-size one starved of
// bits. Going down is quick; coming back needs clear headroom held for a
// while, since every change costs a keyframe.
type resScaler struct {
	scale  int
	since  time.Time // when the current candidate condition started
	moving int       // the divisor that condition points to (0: none)
}

const (
	resMaxScale   = 4
	resMinWidth   = 320  // never shrink below this many pixels across
	resDownBPP    = 0.02 // shrink below this
	resUpBPP      = 0.05 // grow back when the larger size would get this
	resDownAfter  = 2 * time.Second
	resUpAfter    = 10 * time.Second
	resAssumedFPS = 30
)

func bitsPerPixel(bps, w, h, scale int) float64 {
	px := float64(w/scale) * float64(h/scale) * resAssumedFPS
	if px <= 0 {
		return 0
	}
	return float64(bps) / px
}

// update returns the divisor to use for a w x h source at bps.
func (r *resScaler) update(now time.Time, w, h, bps int) int {
	if r.scale == 0 {
		r.scale = 1
	}
	want := r.scale
	switch {
	case r.scale < resMaxScale && w/(r.scale*2) >= resMinWidth && bitsPerPixel(bps, w, h, r.scale) < resDownBPP:
		want = r.scale * 2
	case r.scale > 1 && bitsPerPixel(bps, w, h, r.scale/2) > resUpBPP:
		want = r.scale / 2
	}
	if want == r.scale {
		r.moving = 0
		return r.scale
	}
	if r.moving != want {
		r.moving, r.since = want, now
	}
	wait := resDownAfter
	if want < r.scale {
		wait = resUpAfter
	}
	if now.Sub(r.since) >= wait {
		r.scale, r.moving = want, 0
	}
	return r.scale
}
