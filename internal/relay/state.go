package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zyndata/cross-stream/internal/crypto"
)

// State file names inside a relay state directory.
const (
	KeyFile = "relay.key"
	HubFile = "hub.json"
)

// PairedHub is what the relay keeps after a hub accepted it.
type PairedHub struct {
	HPK     string `json:"hpk"`
	RelayID string `json:"relay_id"`
}

// LoadOrCreateKey loads dir/relay.key or creates it (dir 0700, key 0600).
func LoadOrCreateKey(dir string) (crypto.Keypair, error) {
	path := filepath.Join(dir, KeyFile)
	kp, err := crypto.LoadKeypair(path)
	if err == nil {
		return kp, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return kp, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return kp, fmt.Errorf("relay state dir: %w", err)
	}
	if kp, err = crypto.GenerateKeypair(); err != nil {
		return kp, err
	}
	return kp, crypto.SaveKeypair(path, kp)
}

// IsPaired reports whether dir/hub.json records the hub with this key.
func IsPaired(dir, hpk string) bool {
	b, err := os.ReadFile(filepath.Join(dir, HubFile))
	if err != nil {
		return false
	}
	var h PairedHub
	return json.Unmarshal(b, &h) == nil && h.HPK == hpk
}

// SavePaired writes dir/hub.json (0600).
func SavePaired(dir string, h PairedHub) error {
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("paired hub: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, HubFile), b, 0o600); err != nil {
		return fmt.Errorf("paired hub: %w", err)
	}
	return nil
}

// ForgetPaired deletes dir/hub.json (the hub forgot this relay).
func ForgetPaired(dir string) error {
	if err := os.Remove(filepath.Join(dir, HubFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forget paired hub: %w", err)
	}
	return nil
}
