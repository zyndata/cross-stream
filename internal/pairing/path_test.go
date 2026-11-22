package pairing

import (
	"errors"
	"testing"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
)

func TestPathSettingsValidate(t *testing.T) {
	tests := []struct {
		s  PathSettings
		ok bool
	}{
		{PathSettings{}, true},
		{PathSettings{Priority: "backup", Up: DirOff, Down: DirBackupOnly}, true},
		{PathSettings{Priority: "high"}, false},
		{PathSettings{Down: "maybe"}, false},
	}
	for _, tt := range tests {
		err := tt.s.Validate()
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrSettings)) {
			t.Errorf("%+v: %v", tt.s, err)
		}
	}
}

func TestRegistrySetPath(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	kp, _ := crypto.GenerateKeypair()
	rec, err := r.Add(kp.Public, control.Hello{RelayName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	want := PathSettings{Priority: "backup", CapMB: 50}
	if _, err := r.SetPath("phone", want); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetPath(rec.ID, PathSettings{Up: "sideways"}); !errors.Is(err, ErrSettings) {
		t.Errorf("invalid settings: %v", err)
	}
	if _, err := r.SetPath("nobody", want); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown relay: %v", err)
	}
	r2, err := OpenRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.List()[0].Path; got != want {
		t.Errorf("reloaded path settings %+v, want %+v", got, want)
	}
}
