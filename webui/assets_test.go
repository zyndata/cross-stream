package webui

import (
	"io/fs"
	"regexp"
	"testing"
)

// The built panel must use relative URLs only: under Home Assistant ingress
// it lives below /api/hassio_ingress/<token>/, a prefix the server never sees.
func TestBuiltPanelURLsAreRelative(t *testing.T) {
	b, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Skip("panel not built (npm run build): ", err)
	}
	if m := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?/`).Find(b); m != nil {
		t.Fatalf("absolute URL in dist/index.html: %s", m)
	}
	if !regexp.MustCompile(`src="\./assets/`).Match(b) {
		t.Fatalf("no ./assets/ script in dist/index.html:\n%s", b)
	}
}
