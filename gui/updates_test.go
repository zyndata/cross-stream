package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/zyndata/cross-stream/internal/update"
)

func testUpdater(t *testing.T, version, tag string) *updater {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		name := update.HubArchiveName(tag, goruntime.GOOS, goruntime.GOARCH)
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","assets":[{"name":"` + name +
			`","size":1234,"browser_download_url":"https://github.com/zyndata/cross-stream/releases/download/` + tag + `/` + name + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	u := newUpdater(version)
	u.client = &update.Client{HTTP: srv.Client(), LatestURL: srv.URL, Prefix: update.AssetPrefix}
	u.path = filepath.Join(t.TempDir(), updateFile)
	u.prefs = updatePrefs{}
	u.exe = ""
	return u
}

func TestUpdaterCheck(t *testing.T) {
	tests := []struct {
		name, version, tag string
		release, offered   bool
	}{
		{"newer", "v1.0.0", "v1.0.1", true, true},
		{"same", "v1.0.1", "v1.0.1", true, false},
		{"older", "v1.1.0", "v1.0.1", true, false},
		{"dev build", "dev", "v1.0.1", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := testUpdater(t, tt.version, tt.tag)
			v, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if v.Release != tt.release || (v.Available != nil) != tt.offered || !v.Checked || v.Latest != tt.tag {
				t.Fatalf("view %+v", v)
			}
			if tt.offered && v.Available.Size != 1234 {
				t.Errorf("size %d", v.Available.Size)
			}
			if v.CanInstall {
				t.Error("a GUI without xs-hubctl next to it can install")
			}
		})
	}
}

func TestUpdaterPrefs(t *testing.T) {
	u := testUpdater(t, "v1.0.0", "v1.0.1")
	if !u.View().Auto {
		t.Fatal("the check at start must be on by default")
	}
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.Dismiss(); err != nil {
		t.Fatal(err)
	}
	if err := u.SetAuto(false); err != nil {
		t.Fatal(err)
	}
	v := u.View()
	if v.Auto || !v.Dismissed {
		t.Fatalf("view %+v", v)
	}

	// A fresh start reads both back; a newer release than the dismissed one shows again.
	u2 := testUpdater(t, "v1.0.0", "v1.0.2")
	u2.path = u.path
	u2.prefs = loadUpdatePrefs(u.path)
	if _, err := u2.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := u2.View(); v.Auto || v.Dismissed || v.Available == nil {
		t.Fatalf("after restart %+v", v)
	}
}
