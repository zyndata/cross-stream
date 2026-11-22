package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNotElevated means Apply needs root (Linux) or Administrator (Windows).
var ErrNotElevated = errors.New("installing needs root (Linux: sudo) or Administrator (Windows)")

// ErrCanceled means the user dismissed the elevation prompt.
var ErrCanceled = errors.New("elevation canceled")

// ErrBusy means another update is being installed right now (the GUI and
// xs-hubctl update -install, or two GUIs, at the same time).
var ErrBusy = errors.New("another update is being installed")

// Options of Apply.
type Options struct {
	// GUIPrefix (Linux): the prefix xs-hub-gui is installed under, e.g.
	// /usr/local; "" leaves the GUI alone. Windows installs the GUI with the hub.
	GUIPrefix string
	// Log receives the installer output (nil = discarded).
	Log io.Writer

	// Tests: run replaces the commands, elevated the privilege check,
	// programFiles the Windows install root, lockPath the lock file, hashed
	// runs after the archive was checked.
	run          func(ctx context.Context, dir string, env []string, name string, args ...string) error
	elevated     func() bool
	programFiles string
	lockPath     string
	hashed       func()
}

// Apply installs the hub from archive: it copies the file into a new private
// temporary directory, checks the copy against sum (the original lies in a
// directory the user can write to: it may change after the check), unpacks
// the copy and runs this platform's installer from it, then restarts xs-hubd
// if it was running. It needs root or Administrator, and one Apply at a time
// (ErrBusy).
func Apply(ctx context.Context, archive, sum string, o Options) error {
	elevated := Elevated
	if o.elevated != nil {
		elevated = o.elevated
	}
	if !elevated() {
		return ErrNotElevated
	}
	if !validSum(sum) {
		return fmt.Errorf("%s: bad SHA-256 %q", filepath.Base(archive), sum)
	}
	lockPath := lockFile
	if o.lockPath != "" {
		lockPath = o.lockPath
	}
	unlock, err := lock(lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	dir, err := os.MkdirTemp("", "crossstream-install-")
	if err != nil {
		return fmt.Errorf("temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copied := filepath.Join(dir, filepath.Base(archive))
	got, err := copySum(archive, copied)
	if err != nil {
		return err
	}
	if got != sum {
		return fmt.Errorf("%s: SHA-256 does not match", filepath.Base(archive))
	}
	if o.hashed != nil {
		o.hashed()
	}
	top, err := Unpack(copied, filepath.Join(dir, "x"))
	if err != nil {
		return err
	}
	base := filepath.Base(top)
	if !strings.HasPrefix(base, "crossstream-hub-v") || !strings.HasSuffix(base, "-"+runtime.GOOS+"-"+runtime.GOARCH) {
		return fmt.Errorf("%s is not a hub archive for %s/%s", filepath.Base(archive), runtime.GOOS, runtime.GOARCH)
	}
	return install(ctx, top, o)
}

// copySum copies src to dst (a new file) and returns the SHA-256 of what it
// wrote.
func copySum(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(in, maxArchive+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GUIPrefix returns the install prefix of the executable at exe
// (/usr/local for /usr/local/bin/xs-hub-gui), or "" when it is not in a bin
// directory (a build tree).
func GUIPrefix(exe string) string {
	dir := filepath.Dir(exe)
	if filepath.Base(dir) != "bin" {
		return ""
	}
	return filepath.Dir(dir)
}

// Hubctl returns xs-hubctl next to the executable at exe, or "" when there
// is none (the GUI then cannot install an update itself).
func Hubctl(exe string) string {
	name := "xs-hubctl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// lastLine is the last non-empty line of command output, for an error message.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func runLogged(ctx context.Context, o Options, dir string, env []string, name string, args ...string) error {
	if o.run != nil {
		return o.run(ctx, dir, env, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = o.Log, o.Log
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return nil
}
