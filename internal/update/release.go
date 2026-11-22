// Package update finds a newer CrossStream release on GitHub and installs the
// hub from it: the release document, version comparison, the hub archive of
// this platform, its SHA-256 from SHA256SUMS, unpacking and the platform
// installer (deploy/hub/install.sh, deploy/hub-windows/install.ps1).
//
// Releases are read anonymously from the public repository (docs/decisions.md,
// phase 18 "update notifications"); nothing about the hub is sent.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Repository is the public repository releases are taken from.
const Repository = "zyndata/cross-stream"

// LatestURL is the GitHub API document of the latest release (drafts and
// pre-releases are skipped by GitHub).
const LatestURL = "https://api.github.com/repos/" + Repository + "/releases/latest"

// AssetPrefix is the only place an asset is downloaded from.
const AssetPrefix = "https://github.com/" + Repository + "/releases/download/"

// SumsName is the checksum file every release carries (deploy.yml).
const SumsName = "SHA256SUMS"

// Version is a released X.Y.Z version.
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

var versionRe = regexp.MustCompile(`^v?(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)

// ParseVersion parses "v1.2.3" or "1.2.3". Anything else (a development build
// "v0.0.0-12-gabc", "dev", a dry run) is not a release: ok is false, and such a
// build is never offered an update.
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	n := [3]int{}
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return Version{n[0], n[1], n[2]}, true
}

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Release is what the hub needs from a GitHub release.
type Release struct {
	Version Version `json:"-"`
	Tag     string  `json:"tag"`
	Page    string  `json:"page"`
	Assets  []Asset `json:"assets"`
}

// Asset returns the asset called name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// HubArchiveName is the hub archive of a release for goos/goarch
// (deploy.yml: crossstream-hub-vX.Y.Z-linux-amd64.tar.gz, -windows-amd64.zip).
func HubArchiveName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "crossstream-hub-" + tag + "-" + goos + "-" + goarch + ext
}

// HubArchive returns the hub archive of r for goos/goarch.
func (r Release) HubArchive(goos, goarch string) (Asset, bool) {
	return r.Asset(HubArchiveName(r.Tag, goos, goarch))
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	HTMLURL    string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

// ErrNotRelease means the document is not a usable release.
var ErrNotRelease = errors.New("not a release")

// Parse reads a GitHub release document. Assets that are not served from
// prefix (AssetPrefix in production) are dropped, so nothing outside the
// project's own releases is ever downloaded.
func Parse(doc []byte, prefix string) (Release, error) {
	var g ghRelease
	if err := json.Unmarshal(doc, &g); err != nil {
		return Release{}, fmt.Errorf("release document: %w", err)
	}
	v, ok := ParseVersion(g.Tag)
	if !ok || g.Draft || g.Prerelease {
		return Release{}, fmt.Errorf("release %q: %w", g.Tag, ErrNotRelease)
	}
	r := Release{Version: v, Tag: v.String(), Page: g.HTMLURL}
	if !strings.HasPrefix(r.Page, "https://github.com/"+Repository+"/") {
		r.Page = "https://github.com/" + Repository + "/releases/tag/" + r.Tag
	}
	for _, a := range g.Assets {
		if !strings.HasPrefix(a.URL, prefix) || a.Name == "" || strings.ContainsAny(a.Name, `/\`) || a.Size < 0 {
			continue
		}
		r.Assets = append(r.Assets, Asset(a))
	}
	return r, nil
}

// Newer reports whether r is newer than the installed version. An installed
// version that is not a release (a development build) is never offered one.
func (r Release) Newer(installed string) bool {
	cur, ok := ParseVersion(installed)
	return ok && cur.Less(r.Version)
}

// ParseSums returns the SHA-256 (lowercase hex) of name from a SHA256SUMS
// file ("<hex>  <name>" or "<hex> *<name>" per line).
func ParseSums(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		sum := strings.ToLower(f[0])
		if !validSum(sum) {
			return "", fmt.Errorf("%s: bad checksum for %s", SumsName, name)
		}
		return sum, nil
	}
	return "", fmt.Errorf("%s: no checksum for %s", SumsName, name)
}

func validSum(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
