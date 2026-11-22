package relay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Public != k2.Public {
		t.Fatal("second load returned a different key")
	}
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(dir); err == nil {
		t.Fatal("corrupt key file accepted (must not be silently replaced)")
	}
}

func TestPairedHub(t *testing.T) {
	dir := t.TempDir()
	if IsPaired(dir, "k1") {
		t.Fatal("paired before save")
	}
	if err := SavePaired(dir, PairedHub{HPK: "k1", RelayID: "r1"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		hpk  string
		want bool
	}{{"k1", true}, {"k2", false}, {"", false}}
	for _, tt := range tests {
		if got := IsPaired(dir, tt.hpk); got != tt.want {
			t.Errorf("IsPaired(%q) = %v, want %v", tt.hpk, got, tt.want)
		}
	}
	if err := ForgetPaired(dir); err != nil {
		t.Fatal(err)
	}
	if IsPaired(dir, "k1") {
		t.Fatal("still paired after forget")
	}
	if err := ForgetPaired(dir); err != nil {
		t.Fatalf("forget twice: %v", err)
	}
	if err := SavePaired(filepath.Join(dir, "missing"), PairedHub{HPK: "k"}); err == nil {
		t.Fatal("save into a missing dir succeeded")
	}
}
