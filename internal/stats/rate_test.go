package stats

import (
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	steps := []struct {
		total uint64
		at    time.Duration
		want  float64
	}{
		{100, 0, 0},                  // first sample
		{1100, time.Second, 1000},    // 1000 B in 1 s
		{2100, 3 * time.Second, 500}, // 1000 B in 2 s
		{50, 4 * time.Second, 0},     // counter reset
		{50, 4 * time.Second, 0},     // no time passed
		{1050, 6 * time.Second, 500}, // resumes from the reset
	}
	var r Rate
	for i, s := range steps {
		if got := r.Sample(s.total, t0.Add(s.at)); got != s.want || r.Value() != s.want {
			t.Fatalf("step %d: rate %v (value %v), want %v", i, got, r.Value(), s.want)
		}
	}
}
