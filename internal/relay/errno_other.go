//go:build !windows

package relay

import (
	"errors"
	"syscall"
)

// isRefused reports a refused TCP connect.
func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
