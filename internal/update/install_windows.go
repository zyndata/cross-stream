package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// exitCanceled is ERROR_CANCELLED: the elevation wrapper exits with it when
// the UAC prompt is dismissed.
const exitCanceled = 1223

// Elevated reports whether this process runs with an elevated token.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// install runs install.ps1 from the unpacked zip. xs-hub-gui.exe and
// xs-hubctl.exe may be running (this process is xs-hubctl.exe): Windows
// lets a running executable be renamed, not overwritten, so both move aside
// first and come back if the installer fails. install.ps1 stops the
// service to replace xs-hubd.exe; it is started again if it was running.
func install(ctx context.Context, top string, o Options) error {
	root := os.Getenv("ProgramFiles")
	if o.programFiles != "" {
		root = o.programFiles
	}
	dest := filepath.Join(root, "CrossStream")
	removeOld(dest)
	moved := map[string]string{}
	restore := func() {
		for p, old := range moved {
			if _, err := os.Stat(p); err != nil {
				_ = os.Rename(old, p)
			}
		}
	}
	for _, f := range []string{"xs-hub-gui.exe", "xs-hubctl.exe"} {
		p := filepath.Join(dest, f)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		old := fmt.Sprintf("%s.old-%d", p, time.Now().UnixNano())
		if err := os.Rename(p, old); err != nil {
			restore()
			return fmt.Errorf("move %s aside: %w", f, err)
		}
		moved[p] = old
	}
	script := "$ErrorActionPreference = 'Stop'\n" +
		"$s = Get-Service CrossStreamHub -ErrorAction SilentlyContinue\n" +
		"$was = $s -and $s.Status -eq 'Running'\n" +
		"try { & " + psQuote(filepath.Join(top, "install.ps1")) + " -Bin " + psQuote(top) + " }\n" +
		"finally { if ($was) { Start-Service CrossStreamHub } }"
	if err := runLogged(ctx, o, top, nil, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script); err != nil {
		restore()
		return err
	}
	removeOld(dest)
	return nil
}

// removeOld deletes executables an earlier update moved aside; those still
// running stay until the next update.
func removeOld(dest string) {
	old, _ := filepath.Glob(filepath.Join(dest, "*.exe.old-*"))
	for _, p := range old {
		_ = os.Remove(p)
	}
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// RunElevated runs exe with args elevated: Start-Process -Verb RunAs shows the
// UAC prompt, waits for the process and passes its exit code on.
func RunElevated(ctx context.Context, exe string, args ...string) error {
	script, err := runAsScript(exe, args)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == exitCanceled {
		return ErrCanceled
	}
	if err != nil {
		return fmt.Errorf("elevated %s: %w %s", filepath.Base(exe), err, lastLine(out))
	}
	return nil
}

// runAsScript is the PowerShell that starts exe elevated. Each argument is
// double-quoted for the child's command line (paths with spaces); a double
// quote or a trailing backslash would end that quoting early, so such an
// argument is refused (Windows paths hold neither).
func runAsScript(exe string, args []string) (string, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.Contains(a, `"`) || strings.HasSuffix(a, `\`) {
			return "", fmt.Errorf("elevated %s: argument %q cannot be passed", filepath.Base(exe), a)
		}
		quoted[i] = `"` + a + `"`
	}
	return "try { $p = Start-Process -FilePath " + psQuote(exe) + " -ArgumentList " + psQuote(strings.Join(quoted, " ")) +
		" -Verb RunAs -WindowStyle Hidden -Wait -PassThru } catch { exit " + fmt.Sprint(exitCanceled) + " }\nexit $p.ExitCode", nil
}
