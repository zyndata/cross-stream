package relay

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrNoNetwork is returned by HandleBinder while the platform has not
// reported a network for that kind (or reported it lost).
var ErrNoNetwork = errors.New("relay: network not available")

// HandleBinder binds sockets to networks the platform reports by handle
// (Android: Network.getNetworkHandle(), 0 = lost). Kotlin only reports
// networks; every socket is bound inside Go (a common pattern for multi-network Android apps).
type HandleBinder struct {
	wifi, cell atomic.Int64
	bind       func(handle int64, fd uintptr) error
}

// NewHandleBinder returns a binder calling bind for each socket; nil uses the
// platform call (android_setsocknetwork on Android, ErrNotSupported elsewhere).
func NewHandleBinder(bind func(handle int64, fd uintptr) error) *HandleBinder {
	if bind == nil {
		bind = setSockNetwork
	}
	return &HandleBinder{bind: bind}
}

// SetNetwork records the handle for n and reports whether it changed.
func (b *HandleBinder) SetNetwork(n Network, handle int64) bool {
	if n == NetCellular {
		return b.cell.Swap(handle) != handle
	}
	return b.wifi.Swap(handle) != handle
}

// Handle returns the current handle for n (0 = none).
func (b *HandleBinder) Handle(n Network) int64 {
	if n == NetCellular {
		return b.cell.Load()
	}
	return b.wifi.Load()
}

// Bind implements Binder.
func (b *HandleBinder) Bind(n Network, fd uintptr) error {
	h := b.Handle(n)
	if h == 0 {
		return fmt.Errorf("bind %s socket: %w", n, ErrNoNetwork)
	}
	if err := b.bind(h, fd); err != nil {
		return fmt.Errorf("bind %s socket to network %d: %w", n, h, err)
	}
	return nil
}
