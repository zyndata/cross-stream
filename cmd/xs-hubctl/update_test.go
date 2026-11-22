package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zyndata/cross-stream/internal/update"
)

// fakeGitHub serves a release document, one hub archive for this platform and SHA256SUMS.
func fakeGitHub(t *testing.T, tag string, withArchive bool, sums string) *update.Client {
	t.Helper()
	archive := []byte("hub archive")
	name := update.HubArchiveName(tag, runtime.GOOS, runtime.GOARCH)
	if sums == "" {
		h := sha256.Sum256(archive)
		sums = hex.EncodeToString(h[:]) + "  " + name + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dl := srv.URL + "/dl/"
		switch r.URL.Path {
		case "/latest":
			if tag == "" {
				http.NotFound(w, r)
				return
			}
			assets := fmt.Sprintf(`{"name":"SHA256SUMS","size":%d,"browser_download_url":%q}`, len(sums), dl+"SHA256SUMS")
			if withArchive {
				assets += fmt.Sprintf(`,{"name":%q,"size":%d,"browser_download_url":%q}`, name, len(archive), dl+name)
			}
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"assets":[%s]}`, tag, assets)
		case "/dl/" + name:
			_, _ = w.Write(archive)
		case "/dl/SHA256SUMS":
			_, _ = io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &update.Client{HTTP: srv.Client(), LatestURL: srv.URL + "/latest", Prefix: srv.URL + "/dl/"}
}

func TestUpdateCmd(t *testing.T) {
	tests := []struct {
		name        string
		installed   string
		tag         string // "" = no release published
		withArchive bool
		sums        string
		install     bool
		elevated    bool
		wantOut     string
		wantErr     string
	}{
		{"up to date", "v1.0.1", "v1.0.1", true, "", false, false, "up to date", ""},
		{"newer is shown, not installed", "v1.0.0", "v1.0.1", true, "", false, false, "install it: sudo xs-hubctl update -install", ""},
		{"development build", "v1.0.0-4-gabc1234", "v1.0.1", true, "", false, false, "development build", ""},
		{"no release yet", "v1.0.0", "", true, "", false, false, "", "no release published yet"},
		{"no archive for this platform", "v1.0.0", "v1.0.1", false, "", false, false, "", "has no hub archive"},
		{"install needs root", "v1.0.0", "v1.0.1", true, "", true, false, "", update.ErrNotElevated.Error()},
		{"install refuses a bad download", "v1.0.0", "v1.0.1", true,
			strings.Repeat("0", 64) + "  " + update.HubArchiveName("v1.0.1", runtime.GOOS, runtime.GOARCH) + "\n",
			true, true, "", "SHA-256 does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			u := hubUpdate{c: fakeGitHub(t, tt.tag, tt.withArchive, tt.sums), installed: tt.installed, out: &out,
				elevated: func() bool { return tt.elevated }}
			err := u.run(context.Background(), "", "", tt.install, update.Options{Log: io.Discard})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output %q lacks %q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestUpdateCmdApplyNeedsElevation(t *testing.T) {
	if update.Elevated() {
		t.Skip("running elevated")
	}
	u := hubUpdate{out: io.Discard, elevated: func() bool { return false }}
	err := u.run(context.Background(), "archive.tar.gz", strings.Repeat("0", 64), false, update.Options{Log: io.Discard})
	if !errors.Is(err, update.ErrNotElevated) {
		t.Errorf("-apply unelevated = %v", err)
	}
}

func TestGUIPrefixOf(t *testing.T) {
	for _, v := range []string{"none", ""} {
		if got := guiPrefixOf(v); got != "" {
			t.Errorf("guiPrefixOf(%q) = %q", v, got)
		}
	}
	if got := guiPrefixOf("/opt/cs"); got != "/opt/cs" {
		t.Errorf("explicit prefix = %q", got)
	}
}

// TestUpdateCmdLog: -apply runs as root with a log path the user chose; the
// log is a new file or nothing (an existing file or a planted symlink is
// never written through), and a failure ends up in it for the GUI to show.
func TestUpdateCmdLog(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	apply := []string{"-apply", filepath.Join(dir, "gone.tar.gz"), "-sha256", strings.Repeat("0", 64), "-gui-prefix", "none", "-log"}
	if err := updateCmd(context.Background(), append(apply, existing)); err == nil || !strings.Contains(err.Error(), "log") {
		t.Errorf("existing log file: %v", err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep" {
		t.Errorf("existing file now %q", b)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(existing, link); err == nil {
		if err := updateCmd(context.Background(), append(apply, link)); err == nil || !strings.Contains(err.Error(), "log") {
			t.Errorf("symlinked log file: %v", err)
		}
		if b, _ := os.ReadFile(existing); string(b) != "keep" {
			t.Errorf("symlink target now %q", b)
		}
	}
	fresh := filepath.Join(dir, "install.log")
	if err := updateCmd(context.Background(), append(apply, fresh)); err == nil {
		t.Fatal("-apply of a missing archive succeeded")
	}
	if b, _ := os.ReadFile(fresh); !strings.Contains(string(b), "error:") {
		t.Errorf("log %q lacks the error", b)
	}
}

// TestUpdateCmdRateLimited: GitHub's anonymous limit is per address; the
// message says so instead of a bare HTTP 403.
func TestUpdateCmdRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	u := hubUpdate{c: &update.Client{HTTP: srv.Client(), LatestURL: srv.URL, Prefix: srv.URL + "/dl/"}, installed: "v1.0.0", out: io.Discard, elevated: func() bool { return true }}
	err := u.run(context.Background(), "", "", true, update.Options{Log: io.Discard})
	if !errors.Is(err, update.ErrRateLimited) {
		t.Errorf("run = %v, want ErrRateLimited", err)
	}
}
