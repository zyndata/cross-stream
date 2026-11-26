package netcfg

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// setMark gives the socket the hub mark. EPERM (not root: unit tests) is
// ignored: without root the hub installs no own-traffic rules either.
func setMark(fd int) error {
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, Mark); err != nil && !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("socket mark: %w", err)
	}
	return nil
}

// Control returns a socket Control function that gives every socket the
// hub mark (SO_MARK = Mark) and then runs next (nil = nothing): the
// own-traffic rule `not fwmark Mark lookup 100` leaves the daemon's own
// sockets on the laptop's routes (docs/decisions.md, phase 21).
func Control(next ControlFunc) ControlFunc {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = setMark(int(fd)) }); err != nil {
			return err
		}
		if serr != nil {
			return serr
		}
		if next != nil {
			return next(network, address, c)
		}
		return nil
	}
}

// Dialer returns a net.Dialer whose sockets carry the hub mark.
func Dialer() *net.Dialer { return &net.Dialer{Control: Control(nil)} }

// ListenConfig returns a net.ListenConfig whose sockets carry the hub mark.
func ListenConfig() *net.ListenConfig { return &net.ListenConfig{Control: Control(nil)} }
