//go:build !linux && !windows

package tun

import (
	"errors"
	"runtime"
)

// configure is not implemented outside Linux yet (Windows: phase 16).
func configure(string, Config) error {
	return errors.New("tun: address setup not supported on " + runtime.GOOS)
}

// SetMTU is not implemented outside Linux yet (Windows: phase 16).
func SetMTU(string, int) error {
	return errors.New("tun: MTU change not supported on " + runtime.GOOS)
}
