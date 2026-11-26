//go:build !windows

package netcfg

// LeakGuard is Windows only (WFP leak filters); Linux rejects IPv6 with nft
// and points resolved at the tunnel instead (own.go).
type LeakGuard struct{}

// NewLeakGuard returns nil outside Windows.
func NewLeakGuard() *LeakGuard { return nil }

// Close does nothing outside Windows.
func (*LeakGuard) Close() {}
