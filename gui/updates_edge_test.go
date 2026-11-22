package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/update"
)

// TestUpdaterCheckMessages: what the Settings card says when a check fails.
func TestUpdaterCheckMessages(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"no release yet", http.StatusNotFound, "", "Nie ma jeszcze opublikowanego wydania."},
		{"rate limit", http.StatusForbidden, `{"message":"API rate limit exceeded"}`, "ogranicza zapytania"},
		{"captive portal", http.StatusOK, "<html>Zaloguj się do sieci hotelowej</html>", "Nie udało się sprawdzić"},
		{"server error", http.StatusServiceUnavailable, "", "Nie udało się sprawdzić"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			u := testUpdater(t, "v1.0.0", "v1.0.1")
			u.client.HTTP, u.client.LatestURL = srv.Client(), srv.URL
			v, err := u.Check(context.Background())
			if err == nil || v.State != updIdle || !strings.Contains(v.Error, tt.want) || v.Checked {
				t.Fatalf("view %+v, err %v; want error with %q", v, err, tt.want)
			}
		})
	}
}

// TestUpdaterInstallStalled: the laptop slept (or the network went) in the
// middle of the download: the download ends instead of hanging at the same
// percentage forever, says why, and can be started again.
func TestUpdaterInstallStalled(t *testing.T) {
	u, _ := installUpdaterServing(t, goodSums, func(w http.ResponseWriter, r *http.Request, archive []byte) {
		_, _ = w.Write(archive[:5])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	u.client.Stall = 200 * time.Millisecond
	u.elevate = func(context.Context, string, ...string) error {
		t.Error("elevated after a stalled download")
		return nil
	}
	var e emits
	if u.install(context.Background(), e.add) {
		t.Fatal("install reported success")
	}
	v := u.View()
	if v.State != updIdle || !strings.Contains(v.Error, "uśpienie") || !v.CanInstall {
		t.Fatalf("view %+v", v)
	}
}

// TestUpdaterInstallBusy: xs-hubctl update -install (or another GUI) is
// installing right now; the elevated -apply stops with ErrBusy.
func TestUpdaterInstallBusy(t *testing.T) {
	u, _ := installUpdater(t, goodSums)
	u.elevate = func(_ context.Context, _ string, a ...string) error {
		_ = os.WriteFile(a[len(a)-1], []byte("error: "+update.ErrBusy.Error()+"\n"), 0o644)
		return errors.New("exit status 1")
	}
	var e emits
	if u.install(context.Background(), e.add) {
		t.Fatal("install reported success")
	}
	if v := u.View(); !strings.Contains(v.Error, "Inna instalacja") {
		t.Fatalf("view %+v", v)
	}
}

// TestUpdaterInstallOnce: a double click, or "Check now" while installing,
// changes nothing; the second install does not ask for a password again.
func TestUpdaterInstallOnce(t *testing.T) {
	u, _ := installUpdater(t, goodSums)
	inside := make(chan struct{})
	release := make(chan struct{})
	var elevations atomic.Int32
	u.elevate = func(context.Context, string, ...string) error {
		elevations.Add(1)
		close(inside)
		<-release
		return nil
	}
	var e emits
	done := make(chan bool)
	go func() { done <- u.install(context.Background(), e.add) }()
	<-inside
	if u.install(context.Background(), e.add) {
		t.Error("a second install ran")
	}
	if v, err := u.Check(context.Background()); err != nil || v.State != updInstalling {
		t.Errorf("Check while installing: %+v, %v", v, err)
	}
	close(release)
	if !<-done {
		t.Fatalf("first install failed: %+v", u.View())
	}
	if n := elevations.Load(); n != 1 {
		t.Errorf("elevated %d times", n)
	}
}

// TestUpdaterCheckAtStart: the GUI starts with the session, often before the
// network is up. The check at start tries again a few times, and not at all
// when it is switched off; "no release" and the rate limit are answers, not
// reasons to retry.
func TestUpdaterCheckAtStart(t *testing.T) {
	tests := []struct {
		name      string
		auto      bool
		answers   []int // HTTP status per request; 200 = the release
		wantReqs  int32
		wantFound bool
	}{
		{"network up at once", true, []int{200}, 1, true},
		{"network up on the third try", true, []int{0, 0, 200}, 3, true},
		{"never any network", true, []int{0, 0, 0, 0}, 3, false},
		{"no release yet", true, []int{404, 200}, 1, false},
		{"rate limited", true, []int{403, 200}, 1, false},
		{"switched off", false, []int{200}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := testUpdater(t, "v1.0.0", "v1.0.1")
			good := u.client.LatestURL
			var reqs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := reqs.Add(1)
				code := tt.answers[min(int(n), len(tt.answers))-1]
				switch code {
				case 200:
					http.Redirect(w, r, good, http.StatusTemporaryRedirect)
				case 0: // no network: the connection drops
					hj, _ := w.(http.Hijacker)
					c, _, _ := hj.Hijack()
					_ = c.Close()
				default:
					w.WriteHeader(code)
				}
			}))
			defer srv.Close()
			u.client.HTTP, u.client.LatestURL = &http.Client{}, srv.URL
			u.startRetry = []time.Duration{time.Millisecond, time.Millisecond}
			u.prefs.Auto = &tt.auto
			var e emits
			u.checkAtStart(context.Background(), e.add, func(error) {})
			if got := reqs.Load(); got != tt.wantReqs {
				t.Errorf("%d requests, want %d", got, tt.wantReqs)
			}
			if found := u.View().Available != nil; found != tt.wantFound {
				t.Errorf("found %v, want %v (%+v)", found, tt.wantFound, u.View())
			}
		})
	}
}

// TestUpdaterCheckAtStartStops: the app closes while waiting for the next
// try, or the user checked by hand meanwhile.
func TestUpdaterCheckAtStartStops(t *testing.T) {
	u := testUpdater(t, "v1.0.0", "v1.0.1")
	u.client.LatestURL = "http://127.0.0.1:1/" // nothing listens: unreachable
	u.startRetry = []time.Duration{time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		u.checkAtStart(ctx, func(UpdateView) {}, func(error) {})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the app closed")
	}
}

// TestUpdaterPrefsDamaged: a damaged settings file means the defaults; a
// settings directory that cannot be created is an error, not a crash.
func TestUpdaterPrefsDamaged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, updateFile)
	for _, body := range []string{"{not json", "", `{"auto":"yes"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadUpdatePrefs(p); got.Auto != nil || got.Dismissed != "" {
			t.Errorf("%q read as %+v", body, got)
		}
	}
	u := testUpdater(t, "v1.0.0", "v1.0.1")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	u.path = filepath.Join(blocker, "crossstream", updateFile)
	if err := u.SetAuto(false); err == nil {
		t.Error("saved under a file")
	}
	if u.View().Auto {
		t.Error("the switch is lost for this session too")
	}
}
