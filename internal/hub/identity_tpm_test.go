//go:build tpmsim

package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/simulator"
)

// simTPM keeps one simulator for the test and hands out non-closing views,
// as the hub opens and closes the TPM on every key operation.
func simTPM(t *testing.T) tpmOpener {
	t.Helper()
	sim, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.Close() })
	return func() (transport.TPMCloser, error) { return keepOpen{sim}, nil }
}

type keepOpen struct{ transport.TPM }

func (keepOpen) Close() error { return nil }

func TestHubKeySealedByTPM(t *testing.T) {
	open := simTPM(t)
	dir := t.TempDir()
	k1, err := loadOrCreateHubKey(dir, open)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); !os.IsNotExist(err) {
		t.Fatalf("plain %s written next to the sealed key: %v", KeyFile, err)
	}
	k2, err := loadOrCreateHubKey(dir, open)
	if err != nil || k2 != k1 {
		t.Fatalf("sealed key not reloaded: %v", err)
	}
}

func TestMigrateKeyToTPM(t *testing.T) {
	open := simTPM(t)
	dir := t.TempDir()
	k1, err := loadOrCreateHubKey(dir, noTPM) // an existing file-based hub
	if err != nil {
		t.Fatal(err)
	}
	k2, err := migrateKeyToTPM(dir, open)
	if err != nil || k2 != k1 {
		t.Fatalf("migrate: %v (same key %v)", err, k2 == k1)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); !os.IsNotExist(err) {
		t.Fatalf("%s left after migration: %v", KeyFile, err)
	}
	k3, err := loadOrCreateHubKey(dir, open)
	if err != nil || k3 != k1 {
		t.Fatalf("after migration: %v", err)
	}
	if _, err := migrateKeyToTPM(dir, open); err == nil {
		t.Fatal("second migration succeeded")
	}
}
