package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-tpm/tpm2/transport"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub/tpmkey"
)

func noTPM() (transport.TPMCloser, error) { return nil, tpmkey.ErrNoTPM }

func TestSealedKeyNeedsTPM(t *testing.T) {
	dir := t.TempDir()
	if err := tpmkey.Save(filepath.Join(dir, TPMKeyFile), tpmkey.Blob{Public: []byte{1}, Private: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if err := crypto.SaveKeypair(filepath.Join(dir, KeyFile), mustKeypair(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateHubKey(dir, noTPM); err == nil {
		t.Fatal("sealed key loaded without a TPM (or the stale hub.key used)")
	}
	if _, err := migrateKeyToTPM(dir, noTPM); err == nil {
		t.Fatal("migrated over an existing sealed key")
	}
	if err := os.Remove(filepath.Join(dir, TPMKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateKeyToTPM(dir, noTPM); !errors.Is(err, tpmkey.ErrNoTPM) {
		t.Fatalf("migrate without TPM: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); err != nil {
		t.Fatalf("failed migration removed %s: %v", KeyFile, err)
	}
}

func mustKeypair(t *testing.T) crypto.Keypair {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func TestKeyAndEnrollmentFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	k1, err := loadOrCreateHubKey(dir, noTPM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); err != nil {
		t.Fatalf("no TPM: %s not written: %v", KeyFile, err)
	}
	k2, err := loadOrCreateHubKey(dir, noTPM)
	if err != nil || k1.Public != k2.Public {
		t.Fatalf("key not reused: %v", err)
	}
	if _, err := LoadEnrollment(dir); err == nil {
		t.Fatal("missing enrollment loaded")
	}
	e := Enrollment{Name: "t1", ServerKey: crypto.EncodeKey(k1.Public), Endpoints: []string{"udp://127.0.0.1:51900"},
		TunnelIP: "100.64.0.2", MTU: 1360}
	if err := SaveEnrollment(dir, e); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnrollment(dir)
	if err != nil || got.Name != "t1" || got.TunnelIP != "100.64.0.2" || got.Endpoints[0] != e.Endpoints[0] {
		t.Fatalf("enrollment %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, EnrollmentFile), []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnrollment(dir); err == nil {
		t.Fatal("incomplete enrollment loaded")
	}
}

func TestResolveEndpoints(t *testing.T) {
	tests := []struct {
		name string
		eps  []string
		want string
		ok   bool
	}{
		{"udp first", []string{"tcp://127.0.0.1:1", "udp://127.0.0.1:51900"}, "127.0.0.1:51900", true},
		{"no udp", []string{"wss://example.org/xs"}, "", false},
		{"bad host", []string{"udp://no-such-host.invalid:1"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveEndpoints(tt.eps)
			if (err == nil) != tt.ok || (tt.ok && got[0].String() != tt.want) {
				t.Fatalf("got %v, %v; want %s ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestEnrollWithBadCode(t *testing.T) {
	conn, err := ListenUDP("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollWithCode(t.Context(), t.TempDir(), "XS1.junk", conn, "test", nil); err == nil ||
		!strings.Contains(err.Error(), "enroll") {
		t.Fatalf("bad code: %v", err)
	}
	if _, err := ListenUDP("no-such-interface0"); err == nil {
		t.Fatal("unknown interface accepted")
	}
}

func TestEnrollError(t *testing.T) {
	other := errors.New("bad token")
	tests := []struct {
		name  string
		err   error
		route string
		want  error
	}{
		{"no answer via phone", ErrNoResponse, enrollViaPhone, ErrNoResponse},
		{"no answer via local link", ErrNoResponse, enrollViaLocal, ErrNoAnswerHere},
		{"no answer via own connection", fmt.Errorf("x: %w", ErrNoResponse), enrollViaOwn, ErrNoAnswerHere},
		{"other error keeps its cause", other, enrollViaOwn, other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrollError(tt.err, tt.route)
			if !errors.Is(got, tt.want) || !strings.HasPrefix(got.Error(), "enroll: ") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
