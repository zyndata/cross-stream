package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	// The daemon's own user keeps access to what it writes there.
	if err := os.WriteFile(filepath.Join(dir, "hub.key"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "hub.key")); err != nil {
		t.Fatal(err)
	}
	if err := SecureDir(dir); err != nil { // idempotent
		t.Fatal(err)
	}
}
