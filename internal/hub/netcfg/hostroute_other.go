//go:build !windows

package netcfg

func (HostRoute) native(string) error { return ErrNoHostRoute }
