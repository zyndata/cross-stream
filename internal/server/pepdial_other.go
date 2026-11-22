//go:build !linux

package server

import "syscall"

// tcpBBR is a no-op outside Linux.
func tcpBBR(_, _ string, _ syscall.RawConn) error { return nil }
