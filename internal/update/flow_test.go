package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// recorder stands in for the installer commands.
type recorder struct {
	mu    sync.Mutex
	calls []string
	dirs  []string
	fail  string // command name that fails
}

func (r *recorder) run(_ context.Context, dir string, env []string, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.TrimSpace(strings.Join(env, " ")+" "+filepath.Base(name)+" "+strings.Join(args, " ")))
	r.dirs = append(r.dirs, dir)
	if name == r.fail {
		return errors.New("exit status 1")
	}
	return nil
}

// hubArchive writes a release-shaped hub archive for goos/goarch.
func hubArchive(t *testing.T, tag, goos, goarch string) string {
	t.Helper()
	top := "crossstream-hub-" + tag + "-" + goos + "-" + goarch
	if goos == "windows" {
		return zipFile(t, map[string]string{top + "/install.ps1": "#", top + "/xs-hubd.exe": "new"})
	}
	return tarGz(t, map[string]string{top + "/install.sh": "#!/bin/sh", top + "/bin/xs-hubd": "new", top + "/bin/xs-hubctl": "new"})
}

// TestUpdateFlow walks the whole path a hub takes: release document, its
// archive and SHA256SUMS from a fake GitHub, download, check and install.
func TestUpdateFlow(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("hub updates install on Linux and Windows only")
	}
	tag := "v1.0.1"
	src := hubArchive(t, tag, runtime.GOOS, runtime.GOARCH)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	name := HubArchiveName(tag, runtime.GOOS, runtime.GOARCH)
	files := map[string][]byte{
		"/dl/" + name:    body,
		"/dl/SHA256SUMS": []byte(sumOf(body) + "  " + name + "\n"),
	}
	c, prefix := testServer(t, files)
	files["/latest"] = []byte(fmt.Sprintf(`{"tag_name":%q,"assets":[
		{"name":%q,"size":%d,"browser_download_url":%q},
		{"name":"SHA256SUMS","size":80,"browser_download_url":%q}]}`, tag, name, len(body), prefix+name, prefix+"SHA256SUMS"))

	ctx := context.Background()
	r, err := c.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Newer("v1.0.0") || r.Newer("v1.0.1") || r.Newer("dev") {
		t.Fatalf("Newer wrong for %s", r.Tag)
	}
	a, ok := r.HubArchive(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Fatalf("no %s in %+v", name, r.Assets)
	}
	sum, err := c.Sum(ctx, r, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	path, err := c.Download(ctx, a, sum, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	o := Options{GUIPrefix: "/usr/local", run: rec.run, elevated: func() bool { return true }, programFiles: t.TempDir(), lockPath: testLock(t)}
	if err := Apply(ctx, path, sum, o); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) == 0 {
		t.Fatal("no installer ran")
	}
	want := []string{"XS_IPHONE=no sh ", "/install.sh "}
	if runtime.GOOS == "windows" {
		want = []string{"powershell.exe", "install.ps1"}
	}
	for _, w := range want {
		if !strings.Contains(rec.calls[0], w) {
			t.Errorf("first command %q lacks %q", rec.calls[0], w)
		}
	}
	if got := filepath.Base(rec.dirs[0]); got != "crossstream-hub-"+tag+"-"+runtime.GOOS+"-"+runtime.GOARCH {
		t.Errorf("installer ran in %s", got)
	}
	if _, err := os.Stat(rec.dirs[0]); !os.IsNotExist(err) {
		t.Errorf("unpacked files left behind in %s", rec.dirs[0])
	}
}

func TestApplyRefuses(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("hub updates install on Linux and Windows only")
	}
	good := hubArchive(t, "v1.0.1", runtime.GOOS, runtime.GOARCH)
	goodSum, _ := FileSum(good)
	otherArch := "arm64"
	if runtime.GOARCH == "arm64" {
		otherArch = "amd64"
	}
	wrongPlatform := hubArchive(t, "v1.0.1", runtime.GOOS, otherArch)
	wrongPlatformSum, _ := FileSum(wrongPlatform)
	notHub := zipFile(t, map[string]string{"something-else/install.ps1": "#"})
	notHubSum, _ := FileSum(notHub)

	tests := []struct {
		name     string
		archive  string
		sum      string
		elevated bool
		want     error
	}{
		{"not elevated", good, goodSum, false, ErrNotElevated},
		{"wrong sum", good, strings.Repeat("1", 64), true, nil},
		{"bad sum text", good, "abc", true, nil},
		{"other architecture", wrongPlatform, wrongPlatformSum, true, nil},
		{"not a hub archive", notHub, notHubSum, true, nil},
		{"missing file", filepath.Join(t.TempDir(), "gone.zip"), goodSum, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			o := Options{run: rec.run, elevated: func() bool { return tt.elevated }, programFiles: t.TempDir(), lockPath: testLock(t)}
			err := Apply(context.Background(), tt.archive, tt.sum, o)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("Apply = %v", err)
			}
			if len(rec.calls) != 0 {
				t.Errorf("installer ran: %v", rec.calls)
			}
		})
	}
}

// TestApplyTamperedAfterDownload: the archive changed between the GUI's
// download and the elevated -apply (the hash is checked again).
func TestApplyTamperedAfterDownload(t *testing.T) {
	p := hubArchive(t, "v1.0.1", runtime.GOOS, runtime.GOARCH)
	sum, _ := FileSum(p)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("x")
	_ = f.Close()
	rec := &recorder{}
	if err := Apply(context.Background(), p, sum, Options{run: rec.run, elevated: func() bool { return true }, lockPath: testLock(t)}); err == nil {
		t.Fatal("a changed archive was installed")
	}
	if len(rec.calls) != 0 {
		t.Errorf("installer ran: %v", rec.calls)
	}
}
