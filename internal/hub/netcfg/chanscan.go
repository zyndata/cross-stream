package netcfg

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BSS is one neighbour network from `iw dev <iface> scan`.
type BSS struct {
	MHz    int     // primary channel centre frequency
	Signal float64 // dBm
}

// ParseScan reads the BSS blocks of `iw dev <iface> scan` output (freq and
// signal lines; "freq: 2412" and "freq: 2412.0" both occur).
func ParseScan(out string) []BSS {
	var list []BSS
	var cur *BSS
	for line := range strings.SplitSeq(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "BSS "):
			list = append(list, BSS{Signal: -90})
			cur = &list[len(list)-1]
		case cur == nil:
		case strings.HasPrefix(t, "freq:"):
			if f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(t, "freq:")), 64); err == nil {
				cur.MHz = int(math.Round(f))
			}
		case strings.HasPrefix(t, "signal:"):
			f := strings.Fields(strings.TrimPrefix(t, "signal:"))
			if len(f) > 0 {
				if s, err := strconv.ParseFloat(f[0], 64); err == nil {
					cur.Signal = s
				}
			}
		}
	}
	out2 := make([]BSS, 0, len(list)) // non-nil: an empty scan is still a scan
	for _, b := range list {
		if b.MHz > 0 {
			out2 = append(out2, b)
		}
	}
	return out2
}

// bssWeight is how much a neighbour disturbs us: 1 at -45 dBm or stronger,
// falling linearly to 0.05 at -95 dBm.
func bssWeight(signal float64) float64 {
	return min(1, max(0.05, (signal+95)/50))
}

// overlap is the share of a neighbour on mhz that lands on our channel at
// ch: 2.4 GHz 20 MHz channels overlap up to 4 channel numbers (5 MHz
// apart) away; 5 GHz channels do not overlap (neighbours' wider 40/80 MHz
// channels are not taken into account).
func overlap(ch, mhz int) float64 {
	d := math.Abs(float64(ch-mhz)) / 5
	if ch < 3000 {
		return max(0, 1-d/5)
	}
	if d == 0 {
		return 1
	}
	return 0
}

// channelLoad is the summed weight of the neighbours on or overlapping ch.
func channelLoad(scan []BSS, mhz int) float64 {
	var l float64
	for _, b := range scan {
		l += bssWeight(b.Signal) * overlap(mhz, b.MHz)
	}
	return l
}

// leastLoaded returns the usable preferred channel in [lo, hi) MHz with the
// lowest load (ties: preference order) and a short explanation.
func leastLoaded(info PhyInfo, lo, hi int, prefs []int, scan []BSS) (int, string) {
	type cand struct {
		num  int
		load float64
	}
	var cs []cand
	for _, n := range prefs {
		for _, c := range info.Channels {
			if c.Num == n && c.MHz >= lo && c.MHz < hi && c.usableAP() {
				cs = append(cs, cand{n, channelLoad(scan, c.MHz)})
			}
		}
	}
	if len(cs) == 0 {
		return 0, ""
	}
	best := cs[0]
	parts := make([]string, len(cs))
	for i, c := range cs {
		if c.load < best.load-1e-9 {
			best = c
		}
		parts[i] = fmt.Sprintf("%d: %.2f", c.num, c.load)
	}
	seen := 0
	for _, b := range scan {
		if b.MHz >= lo && b.MHz < hi {
			seen++
		}
	}
	return best.num, fmt.Sprintf("auto: least loaded channel %d (load %s; %d networks on the band)",
		best.num, strings.Join(parts, ", "), seen)
}

// ScanNeighbours runs `iw dev <iface> scan` (xs-hubd is root). The interface
// must be up and not in AP mode; a busy radio is retried once.
func ScanNeighbours(ctx context.Context, run Runner, iface string) ([]BSS, error) {
	out, err := runWith(ctx, run, "", "iw", "dev", iface, "scan")
	if err != nil && strings.Contains(err.Error(), "Network is down") {
		if _, uerr := runWith(ctx, run, "", "ip", "link", "set", "dev", iface, "up"); uerr == nil {
			out, err = runWith(ctx, run, "", "iw", "dev", iface, "scan")
		}
	}
	if err != nil && strings.Contains(err.Error(), "busy") {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		out, err = runWith(ctx, run, "", "iw", "dev", iface, "scan")
	}
	if err != nil {
		return nil, fmt.Errorf("channel scan: %w", err)
	}
	return ParseScan(out), nil
}

// ScanSummary lists the networks per channel for the log, strongest first
// within a channel ("36: 2 (-48 dBm)").
func ScanSummary(info PhyInfo, scan []BSS) string {
	byMHz := map[int][]float64{}
	for _, b := range scan {
		byMHz[b.MHz] = append(byMHz[b.MHz], b.Signal)
	}
	keys := make([]int, 0, len(byMHz))
	for k := range byMHz {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		s := byMHz[k]
		sort.Sort(sort.Reverse(sort.Float64Slice(s)))
		parts = append(parts, fmt.Sprintf("%s: %d (%.0f dBm)", channelName(info, k), len(s), s[0]))
	}
	if len(parts) == 0 {
		return "no networks seen"
	}
	return strings.Join(parts, ", ")
}

func channelName(info PhyInfo, mhz int) string {
	for _, c := range info.Channels {
		if c.MHz == mhz {
			return strconv.Itoa(c.Num)
		}
	}
	return strconv.Itoa(mhz) + " MHz"
}
