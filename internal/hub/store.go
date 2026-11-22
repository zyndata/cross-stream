package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// loadJSON reads dir/name into v; ok is false when the file does not exist.
func loadJSON(dir, name string, v any) (ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hub %s: %w", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("hub %s: %w", name, err)
	}
	return true, nil
}

// saveJSON writes v to dir/name atomically (temp file + rename, mode 0600).
func saveJSON(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("hub %s: %w", name, err)
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("hub %s: %w", name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hub %s: %w", name, err)
	}
	return nil
}
