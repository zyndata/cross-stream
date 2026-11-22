//go:build !windows

package netcfg

// SecureDir does nothing on Linux: the hub creates its state directory
// 0700 and its secret files 0600.
func SecureDir(string) error { return nil }
