package webui

import (
	"fmt"
	"net"
	"net/netip"
)

// DefaultListen is the panel's default address: loopback only, reached
// through an SSH tunnel.
const DefaultListen = "127.0.0.1:8443"

// CheckListen refuses a bind address that is not loopback unless
// allowPublic is set ("host:port"; host "" or 0.0.0.0 = every interface).
func CheckListen(addr string, allowPublic bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q: want host:port: %w", addr, err)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return fmt.Errorf("%q: bad port: %w", addr, err)
	}
	if allowPublic {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Unmap().IsLoopback() {
		return fmt.Errorf("%q is not a loopback address (set web_allow_public: true to bind it, "+
			"and put the panel behind a firewall or reverse proxy)", addr)
	}
	return nil
}
