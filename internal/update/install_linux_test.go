package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallLinux runs install() with real sh on stand-in installers that log
// their arguments and environment; systemctl is recorded, not run.
func TestInstallLinux(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		withGUI    bool
		failScript bool
		want       []string
		restart    bool
	}{
		{"hub and GUI", "/usr/local", true, false,
			[]string{"install.sh BIN XS_IPHONE=no", "gui.sh BIN/xs-hub-gui PREFIX=/usr/local"}, true},
		{"GUI not installed", "", true, false, []string{"install.sh BIN XS_IPHONE=no"}, true},
		{"archive without GUI (arm64)", "/usr/local", false, false, []string{"install.sh BIN XS_IPHONE=no"}, true},
		{"installer fails", "/usr/local", true, true, []string{"install.sh BIN XS_IPHONE=no"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			top := t.TempDir()
			logf := filepath.Join(t.TempDir(), "log")
			exit := "0"
			if tt.failScript {
				exit = "1"
			}
			mustWrite(t, filepath.Join(top, "install.sh"), `echo "install.sh $1 XS_IPHONE=$XS_IPHONE" >>"$LOG"; exit `+exit+"\n")
			mustWrite(t, filepath.Join(top, "gui", "install.sh"), `echo "gui.sh $1 PREFIX=$PREFIX" >>"$LOG"`+"\n")
			if tt.withGUI {
				mustWrite(t, filepath.Join(top, "bin", "xs-hub-gui"), "gui")
			}
			var systemctl []string
			run := func(ctx context.Context, dir string, env []string, name string, args ...string) error {
				if name == "systemctl" {
					systemctl = append(systemctl, strings.Join(args, " "))
					return nil
				}
				cmd := exec.CommandContext(ctx, name, args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), append(env, "LOG="+logf)...)
				return cmd.Run()
			}
			err := install(context.Background(), top, Options{GUIPrefix: tt.prefix, run: run})
			if (err != nil) != tt.failScript {
				t.Fatalf("install = %v", err)
			}
			b, _ := os.ReadFile(logf)
			got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), filepath.Join(top, "bin"), "BIN")), "\n")
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("ran %q, want %q", got, tt.want)
			}
			if restarted := len(systemctl) == 1 && systemctl[0] == "try-restart xs-hubd.service"; restarted != tt.restart {
				t.Errorf("systemctl %q, want restart %v", systemctl, tt.restart)
			}
		})
	}
}

// TestRunElevated runs a stand-in pkexec: only a dismissed dialog (126) is
// "canceled"; refused authorization (127: not allowed, no polkit agent in a
// bare session) is an error carrying pkexec's words, and so is a hub
// without pkexec at all.
func TestRunElevated(t *testing.T) {
	tests := []struct {
		name     string
		script   string // "" = no pkexec on PATH
		canceled bool
		wantErr  string
	}{
		{"authorized", `echo "$@" >"$ARGS"; exit 0`, false, ""},
		{"dialog dismissed", `echo "Error executing command as another user: Request dismissed" >&2; exit 126`, true, ""},
		{"no polkit agent", `echo "Error executing command as another user: No authentication agent found." >&2; exit 127`, false, "No authentication agent"},
		{"not authorized", `echo "Error executing command as another user: Not authorized" >&2; exit 127`, false, "Not authorized"},
		{"installer failed", `echo "error: SHA-256 does not match" >&2; exit 1`, false, "SHA-256 does not match"},
		{"pkexec missing", "", false, "pkexec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			args := filepath.Join(t.TempDir(), "args")
			t.Setenv("ARGS", args)
			if tt.script != "" {
				mustWrite(t, filepath.Join(bin, "pkexec"), "#!/bin/sh\n"+tt.script+"\n")
			}
			t.Setenv("PATH", bin)
			err := RunElevated(context.Background(), "/usr/local/bin/xs-hubctl", "update", "-apply", "/tmp/a b.tar.gz")
			if errors.Is(err, ErrCanceled) != tt.canceled {
				t.Fatalf("RunElevated = %v, canceled want %v", err, tt.canceled)
			}
			switch {
			case tt.wantErr == "" && !tt.canceled && err != nil:
				t.Fatalf("RunElevated = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("RunElevated = %v, want %q", err, tt.wantErr)
			}
			if tt.name == "authorized" {
				if b, _ := os.ReadFile(args); strings.TrimSpace(string(b)) != "/usr/local/bin/xs-hubctl update -apply /tmp/a b.tar.gz" {
					t.Errorf("pkexec got %q", b)
				}
			}
		})
	}
}

// TestLockLinux: a second open file is refused (flock is per open file, so
// this holds within one process too); a symlink planted at the lock path is
// not followed.
func TestLockLinux(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update.lock")
	unlock, err := lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(p); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock = %v, want ErrBusy", err)
	}
	unlock()
	if u, err := lock(p); err != nil {
		t.Fatalf("lock after unlock = %v", err)
	} else {
		u()
	}
	target := filepath.Join(t.TempDir(), "victim")
	mustWrite(t, target, "keep")
	link := filepath.Join(t.TempDir(), "update.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if u, err := lock(link); err == nil {
		u()
		t.Fatal("lock followed a symlink")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
