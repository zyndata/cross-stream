package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockFile is where Apply takes its lock: the hub's state directory
// (Administrators and SYSTEM only, deploy/hub-windows/install.ps1).
var lockFile = filepath.Join(os.Getenv("ProgramData"), "CrossStream", "update.lock")

// lock takes an exclusive, non-blocking LockFileEx on path. Windows drops it
// when the handle closes or the process ends.
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("update lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("update lock: %w", err)
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("update lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
