package netcfg

import (
	"context"
	"log/slog"
	"time"
)

// Watch calls fn every `every` and, debounced by one second, after network
// changes (Linux: rtnetlink route, rule, link and address groups; Windows:
// IP Helper interface, address and route notifications; elsewhere only the
// ticker), until ctx ends. fn's own changes raise events too: they cost one
// more call, so fn must only change what is missing (then it settles).
func Watch(ctx context.Context, every time.Duration, log *slog.Logger, fn func()) {
	ev, err := netlinkEvents(ctx)
	if err != nil {
		log.Warn("netlink events unavailable, checking the network every interval only", "every", every, "err", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		case _, ok := <-ev:
			if !ok {
				ev = nil
			} else if debounce == nil {
				debounce = time.After(time.Second)
			}
		case <-debounce:
			debounce = nil
			fn()
		}
	}
}
