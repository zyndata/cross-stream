package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testLock is a lock file of the test's own (the real one is root's).
func testLock(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "update.lock")
}

// TestDownloadStall: a connection that dies while the computer sleeps or the
// network changes sends nothing more; the download ends with ErrStalled
// instead of hanging, and leaves no file. A slow transfer that keeps sending
// is not a stall.
func TestDownloadStall(t *testing.T) {
	body := []byte(strings.Repeat("x", 64))
	tests := []struct {
		name  string
		serve func(w http.ResponseWriter, r *http.Request)
		stall bool
	}{
		{"silent after half the file", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(body[:32])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}, true},
		{"no answer at all", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, true},
		{"slow but steady", func(w http.ResponseWriter, _ *http.Request) {
			for i := range body {
				_, _ = w.Write(body[i : i+1])
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond) // 640 ms in all, never 200 ms quiet
			}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tt.serve))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), Prefix: srv.URL + "/", Stall: 200 * time.Millisecond}
			dir := t.TempDir()
			start := time.Now()
			_, err := c.Download(context.Background(), Asset{Name: "a.tar.gz", URL: srv.URL + "/a.tar.gz", Size: int64(len(body))}, sumOf(body), dir, nil)
			if tt.stall {
				if !errors.Is(err, ErrStalled) {
					t.Fatalf("Download = %v, want ErrStalled", err)
				}
				if d := time.Since(start); d > 5*time.Second {
					t.Errorf("took %v to notice", d)
				}
			} else if err != nil {
				t.Fatalf("Download = %v", err)
			}
			if left, _ := os.ReadDir(dir); tt.stall && len(left) != 0 {
				t.Errorf("left %d files", len(left))
			}
		})
	}
}

// TestDownloadCanceled: the GUI quits (or the user presses Ctrl-C) mid-way:
// an ordinary error, not a stall, and no partial file.
func TestDownloadCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Prefix: srv.URL + "/"}
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	_, err := c.Download(ctx, Asset{Name: "a.zip", URL: srv.URL + "/a.zip", Size: 100}, strings.Repeat("0", 64), dir, func(int64, int64) { cancel() })
	if err == nil || errors.Is(err, ErrStalled) {
		t.Fatalf("Download = %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %d files", len(left))
	}
}

// TestDownloadNoDirectory: the temporary directory vanished (a cleaner, a
// reboot): an error, nothing written elsewhere.
func TestDownloadNoDirectory(t *testing.T) {
	c, prefix := testServer(t, map[string][]byte{"/dl/a.zip": []byte("a")})
	_, err := c.Download(context.Background(), Asset{Name: "a.zip", URL: prefix + "a.zip", Size: 1}, sumOf([]byte("a")), filepath.Join(t.TempDir(), "gone"), nil)
	if err == nil {
		t.Fatal("downloaded into a missing directory")
	}
}

// TestLatestAnswers: what a check can meet on the way to GitHub.
func TestLatestAnswers(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error // nil: any error
	}{
		{"no release yet", http.StatusNotFound, "{}", ErrNoRelease},
		{"rate limit (403)", http.StatusForbidden, `{"message":"API rate limit exceeded"}`, ErrRateLimited},
		{"rate limit (429)", http.StatusTooManyRequests, "", ErrRateLimited},
		{"captive portal page", http.StatusOK, "<html>Log in to the hotel Wi-Fi</html>", nil},
		{"server error", http.StatusBadGateway, "", nil},
		{"pre-release", http.StatusOK, `{"tag_name":"v2.0.0","prerelease":true}`, ErrNotRelease},
		{"huge document", http.StatusOK, `{"tag_name":"v1.0.0","x":"` + strings.Repeat("a", maxDocument) + `"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), LatestURL: srv.URL, Prefix: srv.URL + "/dl/"}
			_, err := c.Latest(context.Background())
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("Latest = %v, want %v", err, tt.want)
			}
			if tt.want != ErrNoRelease && errors.Is(err, ErrNoRelease) {
				t.Errorf("%v reads as no release", err)
			}
		})
	}
}

// TestRedirectToPlainHTTPRefused: a network that rewrites the request (a
// portal, a proxy) cannot move the client off HTTPS.
func TestRedirectToPlainHTTPRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	c := NewClient("v1.0.0")
	c.HTTP.Transport = tls.Client().Transport
	c.LatestURL = tls.URL
	if r, err := c.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("Latest = %+v, %v", r, err)
	}
}

// TestApplyChecksTheCopyItInstalls: the archive sits in a directory the user
// can write to. Apply hashes a private copy and installs that copy, so a swap
// after the check cannot slip other files into a root install.
func TestApplyChecksTheCopyItInstalls(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("hub updates install on Linux and Windows only")
	}
	p := hubArchive(t, "v1.0.1", runtime.GOOS, runtime.GOARCH)
	sum, _ := FileSum(p)
	evil := hubArchive(t, "v6.6.6", runtime.GOOS, runtime.GOARCH)
	var installed string
	o := Options{
		elevated: func() bool { return true }, programFiles: t.TempDir(), lockPath: testLock(t),
		hashed: func() { // the swap, right after the check
			b, _ := os.ReadFile(evil)
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Error(err)
			}
		},
		run: func(_ context.Context, dir string, _ []string, _ string, _ ...string) error {
			installed = filepath.Base(dir)
			return nil
		},
	}
	if err := Apply(context.Background(), p, sum, o); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(installed, "crossstream-hub-v1.0.1-") {
		t.Errorf("installed from %q", installed)
	}
}

// TestApplyOneAtATime: the GUI's install and xs-hubctl update -install (or two
// GUIs) at the same moment: the second one stops with ErrBusy before it
// touches anything, and can go once the first is done.
func TestApplyOneAtATime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("hub updates install on Linux and Windows only")
	}
	p := hubArchive(t, "v1.0.1", runtime.GOOS, runtime.GOARCH)
	sum, _ := FileSum(p)
	lockPath := testLock(t)
	rec := &recorder{}
	o := Options{run: rec.run, elevated: func() bool { return true }, programFiles: t.TempDir(), lockPath: lockPath}
	unlock, err := lock(lockPath) // the first install, still running
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), p, sum, o); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Apply = %v, want ErrBusy", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("installer ran: %v", rec.calls)
	}
	unlock()
	if err := Apply(context.Background(), p, sum, o); err != nil {
		t.Fatalf("Apply after the first finished = %v", err)
	}
	if len(rec.calls) == 0 {
		t.Error("installer did not run")
	}
}

// TestApplyLeavesNoTemporaryFiles: whatever the outcome, the private copy
// and the unpacked tree are gone afterwards.
func TestApplyLeavesNoTemporaryFiles(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("hub updates install on Linux and Windows only")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // Linux
	t.Setenv("TMP", tmp)    // Windows
	t.Setenv("TEMP", tmp)
	p := hubArchive(t, "v1.0.1", runtime.GOOS, runtime.GOARCH)
	sum, _ := FileSum(p)
	for _, fail := range []string{"", "sh", "powershell.exe"} {
		rec := &recorder{fail: fail}
		_ = Apply(context.Background(), p, sum, Options{run: rec.run, elevated: func() bool { return true }, programFiles: t.TempDir(), lockPath: testLock(t)})
		if left, _ := filepath.Glob(filepath.Join(tmp, "crossstream-install-*")); len(left) != 0 {
			t.Errorf("installer failing %q: left %v", fail, left)
		}
	}
}
