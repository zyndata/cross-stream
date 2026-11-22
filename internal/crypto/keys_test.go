package crypto

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

func TestGenerateKeypair(t *testing.T) {
	a, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if a.Private == b.Private || a.Public == b.Public {
		t.Fatal("two generated keypairs are equal")
	}
	d, err := KeypairFromPrivate(a.Private)
	if err != nil || d != a {
		t.Fatalf("derive mismatch: %v", err)
	}
}

func TestGenerateKeypairRandError(t *testing.T) {
	if _, err := generateKeypair(iotest.ErrReader(errors.New("no entropy"))); err == nil {
		t.Fatal("want error")
	}
}

// RFC 7748 section 6.1 test vector (Alice).
func TestKeypairFromPrivateVector(t *testing.T) {
	priv := mustHex(t, "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	want := mustHex(t, "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
	k, err := KeypairFromPrivate([KeyLen]byte(priv))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Public[:], want) {
		t.Fatalf("public %x", k.Public)
	}
}

func TestKeypairDoesNotLeakPrivate(t *testing.T) {
	k, _ := GenerateKeypair()
	priv := EncodeKey(k.Private)
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "key", k)
	for _, s := range []string{k.String(), fmt.Sprint(k), fmt.Sprintf("%v", k), buf.String()} {
		if strings.Contains(s, priv) {
			t.Fatalf("private key in %q", s)
		}
		if !strings.Contains(s, EncodeKey(k.Public)) {
			t.Fatalf("public key missing in %q", s)
		}
	}
}

func TestParseKey(t *testing.T) {
	k, _ := GenerateKeypair()
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", EncodeKey(k.Public), true},
		{"whitespace", "  " + EncodeKey(k.Public) + "\r\n", true},
		{"not base64", "!!!", false},
		{"short", "AAAA", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseKey(tt.in)
			if tt.ok {
				if err != nil || got != k.Public {
					t.Fatalf("got %x %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrKey) {
				t.Fatalf("err %v, want ErrKey", err)
			}
		})
	}
}

func TestSaveLoadKeypair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.key")
	k, _ := GenerateKeypair()
	if err := SaveKeypair(path, k); err != nil {
		t.Fatal(err)
	}
	// Overwrite works and stays atomic.
	k2, _ := GenerateKeypair()
	if err := SaveKeypair(path, k2); err != nil {
		t.Fatal(err)
	}
	got, err := LoadKeypair(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != k2 {
		t.Fatal("loaded keypair differs")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left: %d entries", len(entries))
	}
}

func TestSaveLoadKeypairErrors(t *testing.T) {
	dir := t.TempDir()
	k, _ := GenerateKeypair()
	if err := SaveKeypair(filepath.Join(dir, "missing", "x.key"), k); err == nil {
		t.Fatal("save into missing dir: want error")
	}
	if err := SaveKeypair(dir, k); err == nil {
		t.Fatal("save over a directory: want error")
	}
	if _, err := LoadKeypair(filepath.Join(dir, "nope.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load missing: %v", err)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeypair(bad); !errors.Is(err, ErrKey) {
		t.Fatalf("load garbage: %v", err)
	}
}
