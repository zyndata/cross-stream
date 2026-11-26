//go:build !linux

package netcfg

import "net"

// Control returns next: only Linux marks the daemon's sockets (Windows has
// no source policy routing; its local sockets are interface-bound).
func Control(next ControlFunc) ControlFunc { return next }

// Dialer returns a plain net.Dialer (marks are Linux only).
func Dialer() *net.Dialer { return &net.Dialer{} }

// ListenConfig returns a plain net.ListenConfig (marks are Linux only).
func ListenConfig() *net.ListenConfig { return &net.ListenConfig{} }
