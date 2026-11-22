package xscore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	tests := []struct{ in, want string }{
		{"scanned XSR1.eyJ2IjoxLCJwc2siOiJ4In0 ok", "scanned <code> ok"},
		{"enroll XSE1.abc-_= done", "enroll <code> done"},
		{`level=INFO psk=secretsecret ssid=x`, `level=INFO psk=<redacted> ssid=x`},
		{`{"psk":"secret","ssid":"x"}`, `{"psk":"<redacted>","ssid":"x"}`},
		{`pair_token=abc token: def`, `pair_token=<redacted> token: <redacted>`},
		{`relay connected hub=dell relay_id=r1`, `relay connected hub=dell relay_id=r1`},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRollingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	r, err := openRolling(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", 59) + "\n"
	for range 3 { // 60, 120 (full) → roll → 60
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Write([]byte("psk=hunter2\n")); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("x")); err == nil {
		t.Fatal("write after close succeeded")
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if len(old) != 120 || string(cur) != line+"psk=<redacted>\n" {
		t.Fatalf("old %d bytes, current %q", len(old), cur)
	}
	r2, err := openRolling(path, 100) // reopen appends
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r2.Close() }()
	if r2.size != int64(len(cur)) {
		t.Fatalf("size %d", r2.size)
	}
}
