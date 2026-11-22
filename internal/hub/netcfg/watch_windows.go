package netcfg

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

// netlinkEvents signals (coalesced) on IPv4 interface, unicast address and
// route changes (NotifyIpInterfaceChange, NotifyUnicastIpAddressChange,
// NotifyRouteChange2). The channel closes when ctx ends. Our own route and
// address changes raise events too; Watch debounces them into one extra
// check, and the checks only change what is missing, so they settle.
func netlinkEvents(ctx context.Context) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)
	notifyMu.Lock()
	notifySubs[ch] = struct{}{}
	notifyMu.Unlock()
	unsubscribe := func() {
		notifyMu.Lock()
		delete(notifySubs, ch)
		notifyMu.Unlock()
	}
	var handles []windows.Handle
	cancelAll := func() {
		for _, h := range handles {
			_ = windows.CancelMibChangeNotify2(h)
		}
	}
	cb := notifyCallback()
	for _, reg := range []struct {
		name string
		fn   func(uint16, uintptr, *windows.Handle) error
	}{
		{"interface", func(f uint16, c uintptr, h *windows.Handle) error {
			return windows.NotifyIpInterfaceChange(f, c, nil, false, h)
		}},
		{"address", func(f uint16, c uintptr, h *windows.Handle) error {
			return windows.NotifyUnicastIpAddressChange(f, c, nil, false, h)
		}},
		{"route", func(f uint16, c uintptr, h *windows.Handle) error {
			return windows.NotifyRouteChange2(f, c, nil, false, h)
		}},
	} {
		var h windows.Handle
		if err := reg.fn(windows.AF_INET, cb, &h); err != nil {
			cancelAll()
			unsubscribe()
			return nil, fmt.Errorf("notify %s changes: %w", reg.name, err)
		}
		handles = append(handles, h)
	}
	go func() {
		<-ctx.Done()
		cancelAll() // waits for running callbacks; none blocks
		unsubscribe()
		close(ch)
	}()
	return ch, nil
}

// The callback trampoline is process-wide: windows.NewCallback slots are
// never freed, so one callback serves every subscriber (both Watch loops of
// a daemon, and tests).
var (
	notifyMu   sync.Mutex
	notifySubs = map[chan struct{}]struct{}{}
	notifyCB   = sync.OnceValue(func() uintptr {
		// The same shape fits all three notifications:
		// VOID CALLBACK fn(PVOID CallerContext, PVOID Row, MIB_NOTIFICATION_TYPE Type).
		return windows.NewCallback(func(_, _, _ uintptr) uintptr {
			notifyMu.Lock()
			for ch := range notifySubs {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			notifyMu.Unlock()
			return 0
		})
	})
)

func notifyCallback() uintptr { return notifyCB() }
