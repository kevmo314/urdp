package server

// rateControl turns a GCC bandwidth estimate into an encoder bitrate cap.
//
// GCC can't tell an idle screen from a slow link: its target never exceeds
// 1.5x the rate actually received, so a static desktop drives it toward the
// minimum and the next burst of motion would be starved. The cap therefore
// follows GCC only while the stream is using a good share of it (or GCC
// sees congestion) and holds steady while the stream is application
// limited.
type rateControl struct {
	min, max int // bps
	cap      int
}

const (
	congestionLoss = 0.05
	// Below this fraction of the cap the stream is application limited
	// and GCC's estimate says more about the content than the link.
	appLimitedShare = 0.5
)

func newRateControl(minBps, maxBps int) *rateControl {
	return &rateControl{min: minBps, max: maxBps, cap: maxBps}
}

// update feeds GCC's target and congestion signals along with the rate
// actually sent, and returns the new cap in bps.
func (r *rateControl) update(target, sent int, overuse bool, loss float64) int {
	switch {
	case overuse || loss > congestionLoss:
		r.cap = min(r.cap, target)
	case float64(sent) >= appLimitedShare*float64(r.cap):
		r.cap = target
	}
	r.cap = min(max(r.cap, r.min), r.max)
	return r.cap
}
