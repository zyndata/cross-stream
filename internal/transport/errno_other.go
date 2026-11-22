//go:build !windows

package transport

import (
	"errors"
	"syscall"
)

// isTransient reports ICMP-caused errors on a connected UDP socket.
func isTransient(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EMSGSIZE)
}
