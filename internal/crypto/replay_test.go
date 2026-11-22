package crypto

import (
	"math/rand/v2"
	"testing"
)

func TestReplayWindowSequence(t *testing.T) {
	tests := []struct {
		name string
		seq  []uint64
		want []bool
	}{
		{"in order", []uint64{0, 1, 2, 3}, []bool{true, true, true, true}},
		{"duplicate zero", []uint64{0, 0}, []bool{true, false}},
		{"duplicate", []uint64{5, 6, 5, 6}, []bool{true, true, false, false}},
		{"out of order", []uint64{10, 3, 7, 1, 3}, []bool{true, true, true, true, false}},
		{"edge of window", []uint64{ReplayWindowSize, 1, 0}, []bool{true, true, false}},
		{"too old", []uint64{20000, 20000 - ReplayWindowSize, 20000 - ReplayWindowSize + 1}, []bool{true, false, true}},
		{"big jump clears ring", []uint64{1, 100000, 1, 100000 - 1, 100000}, []bool{true, true, false, true, false}},
		{"jump exactly ring size", []uint64{64, 64 + ringBlocks*blockBits, 64 + ringBlocks*blockBits - 1}, []bool{true, true, true}},
		{"reserved counter", []uint64{MaxCounter + 1}, []bool{false}},
		{"max counter", []uint64{MaxCounter, MaxCounter, MaxCounter - 1}, []bool{true, false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w ReplayWindow
			for i, n := range tt.seq {
				if got := w.Update(n); got != tt.want[i] {
					t.Fatalf("step %d counter %d: got %v want %v", i, n, got, tt.want[i])
				}
			}
		})
	}
}

func TestReplayWindowCheckDoesNotRecord(t *testing.T) {
	var w ReplayWindow
	for range 2 {
		if !w.Check(5) {
			t.Fatal("check must not record")
		}
	}
	w.Update(5)
	if w.Check(5) {
		t.Fatal("check after update accepted a duplicate")
	}
}

// Compares the bitmap against a map-based model under heavy reordering.
func TestReplayWindowModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var w ReplayWindow
	seen := map[uint64]bool{}
	var highest uint64
	base := uint64(0)
	for i := range 200000 {
		base += uint64(rng.IntN(4))
		n := base + uint64(rng.IntN(3*ReplayWindowSize))
		if n >= 3*ReplayWindowSize/2 {
			n -= uint64(rng.IntN(3 * ReplayWindowSize / 2))
		}
		want := !seen[n] && (n > highest || highest-n < ReplayWindowSize)
		if i == 0 {
			want = true
		}
		if got := w.Update(n); got != want {
			t.Fatalf("step %d counter %d highest %d: got %v want %v", i, n, highest, got, want)
		}
		if want {
			seen[n] = true
			if n > highest {
				highest = n
			}
		}
	}
}

func TestReplayWindowNewer(t *testing.T) {
	var w ReplayWindow
	if !w.Newer(0) {
		t.Error("counter 0 is newer than an empty window")
	}
	w.Update(0)
	if w.Newer(0) || !w.Newer(1) {
		t.Error("after 0: want 0 not newer, 1 newer")
	}
	w.Update(10)
	if w.Newer(5) || w.Newer(10) || !w.Newer(11) {
		t.Error("after 10: want 5 and 10 not newer, 11 newer")
	}
}
