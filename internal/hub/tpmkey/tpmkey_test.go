package tpmkey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.key.tpm")
	in := Blob{Public: []byte{1, 2, 3}, Private: []byte{4, 5}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Public) != string(in.Public) || string(out.Private) != string(in.Private) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

func TestLoadInvalid(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"plain key": "c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0MTI=\n",
		"version":   `{"v":2,"public":"AQ==","private":"AQ=="}`,
		"empty":     `{"v":1,"public":"","private":"AQ=="}`,
		"base64":    `{"v":1,"public":"!!","private":"AQ=="}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file loaded")
	}
}
