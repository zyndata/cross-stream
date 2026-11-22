package transport

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isTransient reports ICMP-caused errors on a connected UDP socket (Windows
// reports port unreachable as WSAECONNRESET).
func isTransient(err error) bool {
	return errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, windows.WSAECONNREFUSED) ||
		errors.Is(err, windows.WSAEHOSTUNREACH) || errors.Is(err, windows.WSAENETUNREACH) ||
		errors.Is(err, windows.WSAEMSGSIZE)
}
