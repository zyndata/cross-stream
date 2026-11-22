//go:build !linux

package server

import "syscall"

// dontFragment is a no-op where IP_MTU_DISCOVER does not exist.
func dontFragment(_, _ string, _ syscall.RawConn) error { return nil }
