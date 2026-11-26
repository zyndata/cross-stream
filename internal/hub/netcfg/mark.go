package netcfg

import "syscall"

// Mark is the socket mark (SO_MARK) of every socket xs-hubd opens on Linux.
// The laptop's own traffic is routed into the tunnel by `not fwmark Mark
// lookup 100`, so the daemon's own sockets (local paths, relay listeners,
// DNS upstreams, captive probes) keep using the laptop's links.
const Mark = 0x5853 // "XS"

// ControlFunc is a net.Dialer / net.ListenConfig Control function.
type ControlFunc = func(network, address string, c syscall.RawConn) error
