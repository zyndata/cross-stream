package stats

import "time"

// Rate turns a growing byte total into bytes per second between samples.
// Not safe for concurrent use.
type Rate struct {
	last  uint64
	at    time.Time
	value float64
}

// Sample records total at now and returns the rate since the previous sample
// (0 on the first sample or if the total went backwards).
func (r *Rate) Sample(total uint64, now time.Time) float64 {
	if !r.at.IsZero() && now.After(r.at) && total >= r.last {
		r.value = float64(total-r.last) / now.Sub(r.at).Seconds()
	} else {
		r.value = 0
	}
	r.last, r.at = total, now
	return r.value
}

// Value returns the rate computed by the last Sample.
func (r *Rate) Value() float64 { return r.value }
