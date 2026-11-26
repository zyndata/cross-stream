package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// pathRow is one path in one sample.
type pathRow struct {
	name    string
	b       bond.PathStats
	dropped uint64 // relay's dropped counter (0 for local uplinks)
}

// pathSample is one status poll reduced to what pathsCmd prints.
type pathSample struct {
	at      time.Time
	rows    []pathRow
	queue   int
	dropped uint64 // bond send-queue drops
	backlog int    // PEP bytes from local apps not sent yet
}

// sampleOf picks the bonded paths (relays and local uplinks) out of a status.
func sampleOf(at time.Time, st hub.Status) pathSample {
	s := pathSample{at: at}
	for _, r := range st.Relays {
		if r.Bond == nil {
			continue
		}
		row := pathRow{name: r.Name, b: *r.Bond}
		if r.Stats != nil {
			row.dropped = r.Stats.Dropped
		}
		s.rows = append(s.rows, row)
	}
	for _, u := range st.Uplinks {
		if u.Bond != nil {
			s.rows = append(s.rows, pathRow{name: u.Name, b: *u.Bond})
		}
	}
	if st.Bond != nil {
		s.queue, s.dropped = st.Bond.Queue, st.Bond.Dropped
	}
	if st.PEP != nil {
		s.backlog = st.PEP.Backlog
	}
	return s
}

const pathsHeader = "t_s,path,name,state,cc,srtt_ms,min_rtt_ms,bdp_rtt_ms,data_rtt_ms,btlbw_mbps,pacing_mbps," +
	"tx_mbps,rx_mbps,inflight_kb,cwnd_kb,inflight_hi_kb,app_limited,loss_pct,lost,retrans,relay_dropped," +
	"queue,queue_dropped,pep_backlog_kb"

// writeSample prints one CSV line per path of cur; counters (lost, retrans,
// drops) are the change since prev (nil: since the path started).
func writeSample(w io.Writer, start time.Time, prev *pathSample, cur pathSample) error {
	old := map[uint8]pathRow{}
	var oldDropped uint64
	if prev != nil {
		for _, r := range prev.rows {
			old[r.b.ID] = r
		}
		oldDropped = prev.dropped
	}
	mbps := func(bytesPerSec uint64) string { return fmt.Sprintf("%.1f", float64(bytesPerSec)*8/1e6) }
	ms := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }
	kb := func(n int) string { return fmt.Sprintf("%.0f", float64(n)/1024) }
	for _, r := range cur.rows {
		o := old[r.b.ID]
		name := strings.ReplaceAll(r.name, ",", " ")
		_, err := fmt.Fprintf(w, "%.1f,%d,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%t,%.1f,%d,%d,%d,%d,%d,%s\n",
			cur.at.Sub(start).Seconds(), r.b.ID, name, r.b.State, r.b.CC,
			ms(r.b.SRTT), ms(r.b.MinRTT), ms(r.b.BDPRTT), ms(r.b.DataRTT),
			mbps(r.b.BtlBw), mbps(r.b.Pacing), mbps(r.b.TxRate), mbps(r.b.RxRate),
			kb(r.b.Inflight), kb(r.b.Cwnd), kb(r.b.InflightHi), r.b.AppLimited, 100*r.b.Loss,
			delta(r.b.Lost, o.b.Lost), delta(r.b.Retransmit, o.b.Retransmit), delta(r.dropped, o.dropped),
			cur.queue, delta(cur.dropped, oldDropped), kb(cur.backlog))
		if err != nil {
			return err
		}
	}
	return nil
}

// delta is cur − prev, or cur when the counter went back (path re-added).
func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// pathsCmd polls xs-hubd and prints the bonded paths' metrics as CSV, one
// line per path per sample (upload diagnostics: what limits a path).
func pathsCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("paths", flag.ContinueOnError)
	every := fs.Duration("every", time.Second, "sample interval")
	count := fs.Int("n", 0, "samples to take (0 = until Ctrl-C)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *every < 100*time.Millisecond {
		return errors.New("paths: -every must be at least 100ms")
	}
	if _, err := fmt.Fprintln(os.Stdout, pathsHeader); err != nil {
		return err
	}
	t := time.NewTicker(*every)
	defer t.Stop()
	var prev *pathSample
	start := time.Now()
	for i := 0; *count == 0 || i < *count; i++ {
		var st hub.Status
		if err := ipc.Call(ctx, sock, hub.MethodStatus, nil, &st); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		cur := sampleOf(time.Now(), st)
		if err := writeSample(os.Stdout, start, prev, cur); err != nil {
			return err
		}
		prev = &cur
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
	return nil
}
