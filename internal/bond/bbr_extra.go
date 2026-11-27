package bond

import "time"

const (
	ackEpochReset  = 1 << 20                // bytes: a long epoch starts over (Linux bbr_ack_epoch_acked_reset_thresh)
	extraAckedTime = 100 * time.Millisecond // extra_acked is at most this long at btlbw (Linux bbr_extra_acked_max_us)
)

// onAcked updates the ACK aggregation estimate for acked bytes; cwnd is
// the path's current in-flight cap.
func (b *bbr) onAcked(now time.Time, acked, cwnd int) {
	bw := b.bw()
	if bw == 0 || acked <= 0 {
		return
	}
	if b.ackEpochAt.IsZero() {
		b.ackEpochAt = now
	}
	expected := uint64(float64(bw) * now.Sub(b.ackEpochAt).Seconds())
	if b.ackEpochAcked <= expected || b.ackEpochAcked+uint64(acked) >= ackEpochReset {
		b.ackEpochAcked, b.ackEpochAt, expected = 0, now, 0
	}
	b.ackEpochAcked += uint64(acked)
	extra := min(b.ackEpochAcked-expected, uint64(cwnd), uint64(float64(bw)*extraAckedTime.Seconds()))
	b.extraAcked.add(b.round, extra)
}

const (
	startupRestartGap = 10 * time.Second // shortest time between two STARTUP restarts
	// startupBusyMin samples that are not app-limited prove a STARTUP's
	// rate. A single one was no proof: in the sim a path with 0.1 Mb/s of
	// chatter had 5 of 145 in 2.5 s, a path carrying 8 Mb/s against an
	// overload ~55 (their share is about the same, 3 %).
	startupBusyMin = 8
)

// maybeRestartStartup enters STARTUP again when a path whose rate was never
// measured gets busy: a sample that is not app-limited while at least half
// its in-flight cap is used (keepalive chatter gave sporadic busy samples
// with a few hundred bytes in flight and used up the restart gap). A path that
// carried only keepalives since it came up (a phone that just joined, a hub
// that just restarted) left STARTUP after startupAppBound of quiet rounds
// with an estimate of its chatter (0.2–1 Mb/s); the first bulk transfer then
// crawled up in PROBE_BW (S23 and modem: ~6 s to the link rate, 2026-11-26).
// A STARTUP with startupBusyMin busy samples (not app-limited) or one that
// ends on loss proves the rate; a path with a proven rate keeps PROBE_BW
// (its estimate survives idle periods). The last sample alone was no proof:
// overloaded paths left STARTUP on an app-limited one too and restarted
// every 10 s.
func (b *bbr) maybeRestartStartup(now time.Time, appLimited bool, inflight int, minRTT time.Duration) {
	if b.mode != bbrProbeBW || b.bwProven || b.stream || appLimited || 2*inflight < b.cwnd(minRTT) ||
		(!b.restartAt.IsZero() && now.Sub(b.restartAt) < startupRestartGap) {
		return
	}
	b.restartStartup(now)
}

// restartStartup enters STARTUP again, from the current estimate.
func (b *bbr) restartStartup(now time.Time) {
	b.mode, b.fullBW, b.fullCnt, b.startupAt, b.startupPace, b.restartAt = bbrStartup, 0, 0, now, 0, now
	b.startupBusy = 0
}
