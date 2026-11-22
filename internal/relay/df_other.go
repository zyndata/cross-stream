//go:build !linux

package relay

// dontFragment is a no-op where IP_MTU_DISCOVER does not exist.
func dontFragment(uintptr) {}
