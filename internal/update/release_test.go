package update

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"v1.2.3", Version{1, 2, 3}, true},
		{"1.0.0", Version{1, 0, 0}, true},
		{" v10.20.30 ", Version{10, 20, 30}, true},
		{"dev", Version{}, false},
		{"v0.0.0-dry.abc1234", Version{}, false},
		{"v1.0.0-5-gabcdef0", Version{}, false},
		{"v1.2", Version{}, false},
		{"", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVersionLess(t *testing.T) {
	tests := []struct {
		a, b Version
		want bool
	}{
		{Version{1, 0, 0}, Version{1, 0, 1}, true},
		{Version{1, 0, 9}, Version{1, 1, 0}, true},
		{Version{1, 9, 9}, Version{2, 0, 0}, true},
		{Version{1, 0, 0}, Version{1, 0, 0}, false},
		{Version{2, 0, 0}, Version{1, 9, 9}, false},
	}
	for _, tt := range tests {
		if got := tt.a.Less(tt.b); got != tt.want {
			t.Errorf("%v.Less(%v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

const doc = `{
  "tag_name": "v1.2.0", "html_url": "https://github.com/zyndata/cross-stream/releases/tag/v1.2.0",
  "draft": false, "prerelease": false,
  "assets": [
    {"name": "crossstream-hub-v1.2.0-linux-amd64.tar.gz", "size": 100,
     "browser_download_url": "https://github.com/zyndata/cross-stream/releases/download/v1.2.0/crossstream-hub-v1.2.0-linux-amd64.tar.gz"},
    {"name": "crossstream-hub-v1.2.0-windows-amd64.zip", "size": 200,
     "browser_download_url": "https://github.com/zyndata/cross-stream/releases/download/v1.2.0/crossstream-hub-v1.2.0-windows-amd64.zip"},
    {"name": "evil.zip", "size": 1, "browser_download_url": "https://example.com/evil.zip"},
    {"name": "SHA256SUMS", "size": 10,
     "browser_download_url": "https://github.com/zyndata/cross-stream/releases/download/v1.2.0/SHA256SUMS"}
  ]
}`

func TestParse(t *testing.T) {
	r, err := Parse([]byte(doc), AssetPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v1.2.0" || r.Version != (Version{1, 2, 0}) || len(r.Assets) != 3 {
		t.Fatalf("got %+v", r)
	}
	if _, ok := r.Asset("evil.zip"); ok {
		t.Error("an asset from another host was kept")
	}
	tests := []struct {
		goos, goarch string
		want         string
		ok           bool
	}{
		{"linux", "amd64", "crossstream-hub-v1.2.0-linux-amd64.tar.gz", true},
		{"windows", "amd64", "crossstream-hub-v1.2.0-windows-amd64.zip", true},
		{"linux", "arm64", "", false},
	}
	for _, tt := range tests {
		a, ok := r.HubArchive(tt.goos, tt.goarch)
		if ok != tt.ok || a.Name != tt.want {
			t.Errorf("HubArchive(%s, %s) = %q, %v", tt.goos, tt.goarch, a.Name, ok)
		}
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct{ name, doc string }{
		{"not json", "<html>"},
		{"no tag", `{"assets": []}`},
		{"dev tag", `{"tag_name": "nightly"}`},
		{"draft", `{"tag_name": "v1.0.0", "draft": true}`},
		{"prerelease", `{"tag_name": "v1.0.0", "prerelease": true}`},
	}
	for _, tt := range tests {
		if _, err := Parse([]byte(tt.doc), AssetPrefix); err == nil {
			t.Errorf("%s: parsed", tt.name)
		}
	}
}

func TestParsePageFallback(t *testing.T) {
	r, err := Parse([]byte(`{"tag_name": "1.0.1", "html_url": "https://example.com/x"}`), AssetPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v1.0.1" || !strings.HasPrefix(r.Page, "https://github.com/zyndata/cross-stream/releases/tag/v1.0.1") {
		t.Errorf("got %+v", r)
	}
}

func TestNewer(t *testing.T) {
	r := Release{Version: Version{1, 2, 0}}
	tests := []struct {
		installed string
		want      bool
	}{
		{"v1.1.9", true},
		{"v1.2.0", false},
		{"v1.3.0", false},
		{"dev", false},
		{"v1.0.0-3-gabc", false},
	}
	for _, tt := range tests {
		if got := r.Newer(tt.installed); got != tt.want {
			t.Errorf("Newer(%q) = %v, want %v", tt.installed, got, tt.want)
		}
	}
}

func TestParseSums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(sum + "  crossstream-hub-v1.2.0-linux-amd64.tar.gz\n" +
		strings.Repeat("CD", 32) + " *crossstream-hub-v1.2.0-windows-amd64.zip\n" +
		"xyz  broken.zip\n")
	tests := []struct {
		name, want string
		ok         bool
	}{
		{"crossstream-hub-v1.2.0-linux-amd64.tar.gz", sum, true},
		{"crossstream-hub-v1.2.0-windows-amd64.zip", strings.Repeat("cd", 32), true},
		{"broken.zip", "", false},
		{"missing.zip", "", false},
	}
	for _, tt := range tests {
		got, err := ParseSums(sums, tt.name)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseSums(%s) = %q, %v", tt.name, got, err)
		}
	}
}

func TestGUIPrefixAndHubArchiveName(t *testing.T) {
	if got := GUIPrefix(filepath.FromSlash("/usr/local/bin/xs-hub-gui")); got != filepath.FromSlash("/usr/local") {
		t.Errorf("GUIPrefix = %q", got)
	}
	if got := GUIPrefix("/home/u/src/gui/build/xs-hub-gui"); got != "" {
		t.Errorf("GUIPrefix of a build tree = %q", got)
	}
	if got := HubArchiveName("v1.0.0", "windows", "amd64"); got != "crossstream-hub-v1.0.0-windows-amd64.zip" {
		t.Errorf("HubArchiveName = %q", got)
	}
}
