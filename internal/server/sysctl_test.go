package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplySysctls(t *testing.T) {
	root := t.TempDir()
	put := func(key, val string) string {
		p := filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(key, ".", "/")))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	same := put("net.ipv4.conf.all.rp_filter", "2")
	differs := put("fs.suid_dumpable", "1")
	want := []Sysctl{
		{"net.ipv4.conf.all.rp_filter", "2", true},
		{"fs.suid_dumpable", "0", false},
		{"net.ipv4.tcp_congestion_control", "bbr", true}, // no such file: cannot be set
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	missing := ApplySysctls(root, want, log)
	if len(missing) != 1 || missing[0].Key != "net.ipv4.tcp_congestion_control" {
		t.Fatalf("missing = %+v", missing)
	}
	for p, v := range map[string]string{same: "2", differs: "0"} {
		b, err := os.ReadFile(p)
		if err != nil || strings.TrimSpace(string(b)) != v {
			t.Errorf("%s = %q (%v), want %s", p, b, err, v)
		}
	}
	for _, s := range HardeningSysctls {
		if s.Key == "" || s.Value == "" {
			t.Errorf("empty sysctl %+v", s)
		}
	}
}
