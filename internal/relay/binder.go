package relay

import (
	"errors"
	"syscall"
)

// Network is the kind of network a socket must use.
type Network int

// Networks a relay binds sockets to.
const (
	NetWiFi     Network = iota // the hub's hotspot (control + data to the hub)
	NetCellular                // mobile data (packets to the server)
)

func (n Network) String() string {
	if n == NetWiFi {
		return "wifi"
	}
	return "cellular"
}

// ErrNotSupported is returned by features this build does not have.
var ErrNotSupported = errors.New("relay: not supported")

// Binder binds a new socket to a network before it connects or sends. It
// runs inside net.Dialer.Control / net.ListenConfig.Control. Linux test
// builds use SO_BINDTODEVICE (DeviceBinder); Android uses
// android_setsocknetwork with network handles (HandleBinder).
type Binder interface {
	Bind(n Network, fd uintptr) error
}

// NoBinder leaves sockets to the OS routing table (unit tests, single-homed hosts).
type NoBinder struct{}

// Bind implements Binder.
func (NoBinder) Bind(Network, uintptr) error { return nil }

// controlFunc returns a Dialer/ListenConfig Control function binding to n;
// cellular tunnel sockets also set DF.
func controlFunc(b Binder, n Network) func(network, address string, c syscall.RawConn) error {
	return bindFunc(b, n, n == NetCellular)
}

// bindFunc binds to n and, with df, sets Don't Fragment.
func bindFunc(b Binder, n Network, df bool) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var berr error
		if err := c.Control(func(fd uintptr) {
			if berr = b.Bind(n, fd); berr == nil && df {
				dontFragment(fd)
			}
		}); err != nil {
			return err
		}
		return berr
	}
}
