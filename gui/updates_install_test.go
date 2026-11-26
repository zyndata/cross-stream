package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/zyndata/cross-stream/internal/update"
)

// installUpdater is an updater whose GUI sits in <tmp>/bin next to an
// xs-hubctl, offered v1.0.1 by a fake GitHub with a real archive and sums.
func installUpdater(t *testing.T, sums func(name, sum string) string) (*updater, []byte) {
	t.Helper()
	return installUpdaterServing(t, sums, func(w http.ResponseWriter, _ *http.Request, archive []byte) { _, _ = w.Write(archive) })
}

// installUpdaterServing is installUpdater with the archive download served by serve.
func installUpdaterServing(t *testing.T, sums func(name, sum string) string, serve func(w http.ResponseWriter, r *http.Request, archive []byte)) (*updater, []byte) {
	t.Helper()
	if goruntime.GOOS != "linux" && goruntime.GOOS != "windows" {
		t.Skip("the GUI installs updates on Linux and Windows only")
	}
	archive := []byte("hub archive bytes")
	h := sha256.Sum256(archive)
	tag := "v1.0.1"
	name := update.HubArchiveName(tag, goruntime.GOOS, goruntime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dl := srv.URL + "/dl/"
		switch r.URL.Path {
		case "/latest":
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/zyndata/cross-stream/releases/tag/%s","assets":[
				{"name":%q,"size":%d,"browser_download_url":%q},
				{"name":"SHA256SUMS","size":100,"browser_download_url":%q}]}`, tag, tag, name, len(archive), dl+name, dl+"SHA256SUMS")
		case "/dl/" + name:
			serve(w, r, archive)
		case "/dl/SHA256SUMS":
			_, _ = w.Write([]byte(sums(name, hex.EncodeToString(h[:]))))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	exe, ctl := filepath.Join(root, "bin", "xs-hub-gui"), filepath.Join(root, "bin", "xs-hubctl")
	if goruntime.GOOS == "windows" {
		exe, ctl = exe+".exe", ctl+".exe"
	}
	for _, f := range []string{exe, ctl} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	u := newUpdater("v1.0.0", testTexts(langPL))
	u.client = &update.Client{HTTP: srv.Client(), LatestURL: srv.URL + "/latest", Prefix: srv.URL + "/dl/"}
	u.path = filepath.Join(t.TempDir(), updateFile)
	u.prefs = updatePrefs{}
	u.exe = exe
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := u.View(); !v.CanInstall || v.Available == nil || v.Available.Tag != tag {
		t.Fatalf("not installable: %+v", v)
	}
	return u, archive
}

func goodSums(name, sum string) string { return sum + "  " + name + "\n" }

// emits collects the views sent to the frontend.
type emits struct {
	mu    sync.Mutex
	views []UpdateView
}

func (e *emits) add(v UpdateView) {
	e.mu.Lock()
	e.views = append(e.views, v)
	e.mu.Unlock()
}

func (e *emits) states() string {
	var s []string
	for _, v := range e.views {
		if len(s) == 0 || s[len(s)-1] != v.State {
			s = append(s, v.State)
		}
	}
	return strings.Join(s, ">")
}

func TestUpdaterInstall(t *testing.T) {
	u, archive := installUpdater(t, goodSums)
	var args []string
	u.elevate = func(_ context.Context, exe string, a ...string) error {
		args = append([]string{filepath.Base(exe)}, a...)
		// The archive is there, whole, while xs-hubctl -apply runs.
		path := a[2]
		b, err := os.ReadFile(path)
		if err != nil || string(b) != string(archive) {
			t.Errorf("archive at -apply: %q, %v", b, err)
		}
		return nil
	}
	var e emits
	if !u.install(context.Background(), e.add) {
		t.Fatalf("install failed: %+v", u.View())
	}
	h := sha256.Sum256(archive)
	wantPrefix := filepath.Dir(filepath.Dir(u.exe))
	if goruntime.GOOS == "windows" {
		wantPrefix = "none"
	}
	want := []string{"update", "-apply", "", "-sha256", hex.EncodeToString(h[:]), "-gui-prefix", wantPrefix, "-log"}
	if len(args) != 10 || !strings.HasPrefix(args[0], "xs-hubctl") {
		t.Fatalf("elevated %q", args)
	}
	for i, w := range want {
		if w != "" && args[i+1] != w {
			t.Errorf("arg %d = %q, want %q (%q)", i+1, args[i+1], w, args)
		}
	}
	if !strings.HasSuffix(args[3], update.HubArchiveName("v1.0.1", goruntime.GOOS, goruntime.GOARCH)) {
		t.Errorf("-apply %q", args[3])
	}
	if got := e.states(); got != "downloading>installing" {
		t.Errorf("states %s", got)
	}
	if _, err := os.Stat(args[3]); !os.IsNotExist(err) {
		t.Errorf("downloaded archive left behind: %v", err)
	}
}

func TestUpdaterInstallFails(t *testing.T) {
	tests := []struct {
		name    string
		sums    func(name, sum string) string
		elevate func(logPath string) error
		want    string
	}{
		{"canceled", goodSums, func(string) error { return update.ErrCanceled }, "Instalacja anulowana."},
		{"installer error from the log", goodSums, func(logPath string) error {
			_ = os.WriteFile(logPath, []byte("copying\nerror: install.sh: exit status 1\n"), 0o644)
			return errors.New("exit status 1")
		}, "error: install.sh: exit status 1"},
		{"no log", goodSums, func(string) error { return errors.New("pkexec: boom") }, "pkexec: boom"},
		{"checksum mismatch", func(name, _ string) string { return strings.Repeat("0", 64) + "  " + name + "\n" },
			nil, "Pobieranie nie powiodło się"},
		{"no checksum", func(string, string) string { return "" }, nil, "sumy kontrolnej"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := installUpdater(t, tt.sums)
			u.elevate = func(_ context.Context, _ string, a ...string) error {
				if tt.elevate == nil {
					t.Error("elevated after a failed download")
					return nil
				}
				return tt.elevate(a[len(a)-1])
			}
			var e emits
			if u.install(context.Background(), e.add) {
				t.Fatal("install reported success")
			}
			v := u.View()
			if v.State != updIdle || !strings.Contains(v.Error, tt.want) {
				t.Errorf("view %+v, want error with %q", v, tt.want)
			}
			// A second attempt is possible after a failure.
			if !v.CanInstall {
				t.Error("cannot retry")
			}
		})
	}
}
