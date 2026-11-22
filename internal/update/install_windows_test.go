package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestInstallWindows: the running GUI and xs-hubctl move aside, install.ps1
// (stood in for by the runner) copies the new files, and the moved files are
// cleaned up — or put back when the installer fails.
func TestInstallWindows(t *testing.T) {
	tests := []struct {
		name    string
		fail    bool
		wantGUI string
	}{
		{"installed", false, "new-gui"},
		{"installer fails", true, "old-gui"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf := t.TempDir()
			dest := filepath.Join(pf, "CrossStream")
			top := t.TempDir()
			writeFile(t, filepath.Join(dest, "xs-hub-gui.exe"), "old-gui")
			writeFile(t, filepath.Join(dest, "xs-hubctl.exe"), "old-ctl")
			writeFile(t, filepath.Join(dest, "xs-hubctl.exe.old-1"), "from an earlier update")
			var script string
			run := func(_ context.Context, _ string, _ []string, name string, args ...string) error {
				if name != "powershell.exe" {
					t.Errorf("ran %s", name)
				}
				script = args[len(args)-1]
				if _, err := os.Stat(filepath.Join(dest, "xs-hub-gui.exe")); err == nil {
					t.Error("xs-hub-gui.exe still in place while install.ps1 runs")
				}
				if tt.fail {
					return errors.New("exit status 1")
				}
				writeFile(t, filepath.Join(dest, "xs-hub-gui.exe"), "new-gui")
				writeFile(t, filepath.Join(dest, "xs-hubctl.exe"), "new-ctl")
				return nil
			}
			err := install(context.Background(), top, Options{run: run, programFiles: pf})
			if (err != nil) != tt.fail {
				t.Fatalf("install = %v", err)
			}
			for _, want := range []string{psQuote(filepath.Join(top, "install.ps1")) + " -Bin " + psQuote(top), "Start-Service CrossStreamHub", "finally"} {
				if !strings.Contains(script, want) {
					t.Errorf("script lacks %q:\n%s", want, script)
				}
			}
			if b, _ := os.ReadFile(filepath.Join(dest, "xs-hub-gui.exe")); string(b) != tt.wantGUI {
				t.Errorf("xs-hub-gui.exe = %q, want %q", b, tt.wantGUI)
			}
			left, _ := filepath.Glob(filepath.Join(dest, "*.old-*"))
			if !tt.fail && len(left) != 0 {
				t.Errorf("moved files left: %v", left)
			}
		})
	}
}

func TestPSQuote(t *testing.T) {
	if got := psQuote(`D:\Data\O'Brien\x`); got != `'D:\Data\O''Brien\x'` {
		t.Errorf("psQuote = %s", got)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInstallWindowsMoveAsideFails: xs-hubctl.exe is held open without
// delete sharing (an antivirus scan, a second xs-hubctl): the GUI, already
// moved, goes back and the installer never runs.
func TestInstallWindowsMoveAsideFails(t *testing.T) {
	pf := t.TempDir()
	dest := filepath.Join(pf, "CrossStream")
	writeFile(t, filepath.Join(dest, "xs-hub-gui.exe"), "old-gui")
	ctl := filepath.Join(dest, "xs-hubctl.exe")
	writeFile(t, ctl, "old-ctl")
	name, _ := windows.UTF16PtrFromString(ctl)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ran := false
	run := func(context.Context, string, []string, string, ...string) error { ran = true; return nil }
	if err := install(context.Background(), t.TempDir(), Options{run: run, programFiles: pf}); err == nil {
		t.Fatal("install went on with xs-hubctl.exe locked")
	}
	if ran {
		t.Error("install.ps1 ran")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "xs-hub-gui.exe")); string(b) != "old-gui" {
		t.Errorf("xs-hub-gui.exe = %q, want it back", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dest, "*.old-*")); len(left) != 0 {
		t.Errorf("moved files left: %v", left)
	}
}

// TestInstallWindowsOldFilesStillRunning: a GUI moved aside by the last
// update may still run (removal fails); the update goes on anyway.
func TestInstallWindowsOldFilesStillRunning(t *testing.T) {
	pf := t.TempDir()
	dest := filepath.Join(pf, "CrossStream")
	old := filepath.Join(dest, "xs-hub-gui.exe.old-1")
	writeFile(t, old, "running")
	name, _ := windows.UTF16PtrFromString(old)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, string, []string, string, ...string) error { return nil }
	if err := install(context.Background(), t.TempDir(), Options{run: run, programFiles: pf}); err != nil {
		t.Fatalf("install = %v", err)
	}
	_ = windows.CloseHandle(h)
	removeOld(dest) // the next update clears it
	if _, err := os.Stat(old); err == nil {
		t.Error("old file not removed once it was free")
	}
}

// TestRunAsScript: the command line the UAC prompt starts. Temp paths hold
// spaces and apostrophes (user names like "Jan Kowalski", "O'Brien"); a double quote or
// a trailing backslash would break the child's quoting and is refused.
func TestRunAsScript(t *testing.T) {
	exe := `D:\Program Files\CrossStream\xs-hubctl.exe`
	script, err := runAsScript(exe, []string{"update", "-apply", `D:\Temp\Jan Kowalski\O'Brien\a.zip`, "-gui-prefix", "none"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`-FilePath 'D:\Program Files\CrossStream\xs-hubctl.exe'`,
		`-ArgumentList '"update" "-apply" "D:\Temp\Jan Kowalski\O''Brien\a.zip" "-gui-prefix" "none"'`,
		"-Verb RunAs", "-Wait", "exit 1223", "exit $p.ExitCode",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %s:\n%s", want, script)
		}
	}
	for _, bad := range []string{`a"b`, `D:\dir\`, ""} {
		if _, err := runAsScript(exe, []string{"-log", bad}); err == nil {
			t.Errorf("argument %q accepted", bad)
		}
	}
}

// TestLockWindows: a second handle in the same process is refused too, and
// the lock goes with the handle.
func TestLockWindows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "update.lock") // the directory is created
	unlock, err := lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(p); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock = %v, want ErrBusy", err)
	}
	unlock()
	unlock2, err := lock(p)
	if err != nil {
		t.Fatalf("lock after unlock = %v", err)
	}
	unlock2()
}
