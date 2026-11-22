package relay

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isRefused reports a refused TCP connect (Windows uses WSAECONNREFUSED).
func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, windows.WSAECONNREFUSED)
}
