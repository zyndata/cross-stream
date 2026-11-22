package update

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile is where Apply takes its lock (root only: /run is root's).
const lockFile = "/run/crossstream-update.lock"

// lock takes an exclusive, non-blocking flock on path. The kernel drops it
// when the process ends, so a crashed install never leaves a stale lock.
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("update lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("update lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
