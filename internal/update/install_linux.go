package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Elevated reports whether this process runs as root.
func Elevated() bool { return os.Geteuid() == 0 }

// install runs deploy/hub/install.sh from the unpacked archive (binaries,
// unit, polkit rule; iPhone packages are not asked), the GUI installer when
// the GUI is installed and the archive has one, and restarts xs-hubd only if
// it was running (systemctl try-restart).
func install(ctx context.Context, top string, o Options) error {
	if err := runLogged(ctx, o, top, []string{"XS_IPHONE=no"}, "sh", filepath.Join(top, "install.sh"), filepath.Join(top, "bin")); err != nil {
		return err
	}
	gui := filepath.Join(top, "bin", "xs-hub-gui")
	if _, err := os.Stat(gui); o.GUIPrefix != "" && err == nil {
		if err := runLogged(ctx, o, top, []string{"PREFIX=" + o.GUIPrefix}, "sh", filepath.Join(top, "gui", "install.sh"), gui); err != nil {
			return err
		}
	}
	return runLogged(ctx, o, top, nil, "systemctl", "try-restart", "xs-hubd.service")
}

func hideWindow(*exec.Cmd) {}

// RunElevated runs exe with args as root through pkexec (polkit asks for the
// password in the desktop session). pkexec exits 126 when the dialog is
// dismissed (ErrCanceled) and 127 when authorization failed for another
// reason (not allowed, no polkit agent running): that one is an error with
// pkexec's own words.
func RunElevated(ctx context.Context, exe string, args ...string) error {
	out, err := exec.CommandContext(ctx, "pkexec", append([]string{exe}, args...)...).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 126 {
		return ErrCanceled
	}
	if err != nil {
		return fmt.Errorf("pkexec: %w: %s", err, lastLine(out))
	}
	return nil
}
