package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T, files map[string][]byte) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	prefix := srv.URL + "/dl/"
	return &Client{HTTP: srv.Client(), LatestURL: srv.URL + "/latest", Prefix: prefix}, prefix
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestLatestSumDownload(t *testing.T) {
	archive := []byte("archive bytes")
	name := "crossstream-hub-v1.0.1-linux-amd64.tar.gz"
	files := map[string][]byte{
		"/dl/" + name:      archive,
		"/dl/SHA256SUMS":   []byte(sumOf(archive) + "  " + name + "\n"),
		"/dl/other.tar.gz": []byte("x"),
	}
	c, prefix := testServer(t, files)
	files["/latest"] = []byte(fmt.Sprintf(`{"tag_name":"v1.0.1","assets":[
		{"name":%q,"size":%d,"browser_download_url":%q},
		{"name":"SHA256SUMS","size":1,"browser_download_url":%q}]}`,
		name, len(archive), prefix+name, prefix+"SHA256SUMS"))

	ctx := context.Background()
	r, err := c.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.HubArchive("linux", "amd64")
	if !ok {
		t.Fatalf("no archive in %+v", r)
	}
	sum, err := c.Sum(ctx, r, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var last int64
	path, err := c.Download(ctx, a, sum, dir, func(done, _ int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, archive) || last != int64(len(archive)) {
		t.Errorf("file %q, progress %d", b, last)
	}

	tests := []struct {
		name  string
		asset Asset
		want  string
	}{
		{"wrong sum", a, strings.Repeat("0", 64)},
		{"wrong size", Asset{Name: a.Name, URL: a.URL, Size: 999}, sum},
		{"other host", Asset{Name: a.Name, URL: "https://example.com/" + a.Name}, sum},
		{"missing", Asset{Name: "gone.zip", URL: prefix + "gone.zip"}, sum},
	}
	for _, tt := range tests {
		if _, err := c.Download(ctx, tt.asset, tt.want, t.TempDir(), nil); err == nil {
			t.Errorf("%s: downloaded", tt.name)
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("files left: %d", len(left))
	}
}

func TestLatestNotFound(t *testing.T) {
	c, _ := testServer(t, map[string][]byte{})
	if _, err := c.Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Errorf("a 404 gave %v, want ErrNoRelease", err)
	}
}

func tarGz(t *testing.T, entries map[string]string, extra ...*tar.Header) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	for _, h := range extra {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func zipFile(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnpack(t *testing.T) {
	top := "crossstream-hub-v1.0.1-linux-amd64"
	tests := []struct {
		name    string
		archive func(t *testing.T) string
		ok      bool
	}{
		{"tar", func(t *testing.T) string {
			return tarGz(t, map[string]string{top + "/install.sh": "#!/bin/sh", top + "/bin/xs-hubd": "elf"})
		}, true},
		{"zip", func(t *testing.T) string {
			return zipFile(t, map[string]string{top + "/install.ps1": "x", top + "/xs-hubd.exe": "pe"})
		}, true},
		{"tar escape", func(t *testing.T) string { return tarGz(t, map[string]string{"../evil": "x"}) }, false},
		{"tar absolute", func(t *testing.T) string { return tarGz(t, map[string]string{"/etc/evil": "x"}) }, false},
		{"zip escape", func(t *testing.T) string { return zipFile(t, map[string]string{top + "/../../evil": "x"}) }, false},
		{"two tops", func(t *testing.T) string { return zipFile(t, map[string]string{"a/x": "1", "b/y": "2"}) }, false},
		{"symlink", func(t *testing.T) string {
			return tarGz(t, nil, &tar.Header{Name: top + "/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
		}, false},
		{"empty", func(t *testing.T) string { return zipFile(t, nil) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			got, err := Unpack(tt.archive(t), dir)
			if (err == nil) != tt.ok {
				t.Fatalf("Unpack = %q, %v", got, err)
			}
			if tt.ok && got != filepath.Join(dir, top) {
				t.Errorf("top = %q", got)
			}
		})
	}
}

func TestApplyNeedsElevationOrSum(t *testing.T) {
	err := Apply(context.Background(), zipFile(t, map[string]string{"x/y": "1"}), strings.Repeat("0", 64), Options{})
	if err == nil {
		t.Fatal("Apply of a wrong archive succeeded")
	}
}
