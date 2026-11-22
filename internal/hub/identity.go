package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/go-tpm/tpm2/transport"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/hub/tpmkey"
)

// Files in the hub state directory.
const (
	KeyFile        = "hub.key"     // hub static X25519 keypair (0600)
	TPMKeyFile     = "hub.key.tpm" // the same key sealed by the TPM (phase 14), replaces hub.key
	RelayKeyFile   = "relay.key"   // hub control-channel keypair for relays (0600)
	EnrollmentFile = "server.json" // server key, endpoints, tunnel address
)

// Enrollment is what a hub keeps after an enrollment.
type Enrollment struct {
	Name      string   `json:"name"`
	ServerKey string   `json:"server_key"` // base64 X25519
	Endpoints []string `json:"endpoints"`
	TunnelIP  string   `json:"tunnel_ip"`
	MTU       int      `json:"mtu"`
	// Token is kept only while a re-enrollment is pending: the new server
	// has not answered yet (its answer may have been lost on the way). The
	// tunnel sends it with every handshake until the server answers, then it
	// is dropped; TunnelIP and MTU are the previous server's until then.
	Token string `json:"token,omitempty"`
	// Pending marks an RPC result whose enrollment is pending (never saved;
	// the token is not returned).
	Pending bool `json:"pending,omitempty"`
}

// pending reports whether the server has not confirmed the enrollment yet.
func (e Enrollment) pending() bool { return e.Token != "" }

// result is e as returned over IPC: no token, Pending set instead.
func (e Enrollment) result() Enrollment {
	e.Pending, e.Token = e.pending(), ""
	return e
}

// LoadOrCreateKey returns the hub keypair from dir: unsealed from
// hub.key.tpm, else read from hub.key, else generated, sealed by the TPM
// when it can be opened (root, /dev/tpmrm0), and saved (hub.key otherwise).
func LoadOrCreateKey(dir string) (crypto.Keypair, error) {
	return loadOrCreateHubKey(dir, tpmkey.Open)
}

// tpmOpener opens the TPM (tests use the simulator).
type tpmOpener func() (transport.TPMCloser, error)

func loadOrCreateHubKey(dir string, open tpmOpener) (crypto.Keypair, error) {
	sealed := filepath.Join(dir, TPMKeyFile)
	if _, err := os.Stat(sealed); err == nil {
		return unsealKey(sealed, open)
	}
	kp, err := crypto.LoadKeypair(filepath.Join(dir, KeyFile))
	if err == nil {
		return kp, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	t, err := open()
	if err != nil { // no TPM, or not allowed to use it: the file
		return loadOrCreateKeyFile(dir, KeyFile)
	}
	defer func() { _ = t.Close() }()
	if kp, err = crypto.GenerateKeypair(); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub state dir: %w", err)
	}
	if err := sealKey(t, sealed, kp); err != nil {
		return crypto.Keypair{}, err
	}
	return kp, nil
}

func unsealKey(path string, open tpmOpener) (crypto.Keypair, error) {
	b, err := tpmkey.Load(path)
	if err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	t, err := open()
	if err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key is sealed by a TPM: %w", err)
	}
	defer func() { _ = t.Close() }()
	priv, err := tpmkey.Unseal(t, b)
	if err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	if len(priv) != crypto.KeyLen {
		return crypto.Keypair{}, fmt.Errorf("hub key: sealed key has %d bytes", len(priv))
	}
	return crypto.KeypairFromPrivate([crypto.KeyLen]byte(priv))
}

// sealKey seals kp into path and checks that it unseals to the same key.
func sealKey(t transport.TPM, path string, kp crypto.Keypair) error {
	b, err := tpmkey.Seal(t, kp.Private[:])
	if err != nil {
		return fmt.Errorf("hub key: %w", err)
	}
	if got, err := tpmkey.Unseal(t, b); err != nil || !bytes.Equal(got, kp.Private[:]) {
		return fmt.Errorf("hub key: sealed key does not unseal (%v)", err)
	}
	if err := tpmkey.Save(path, b); err != nil {
		return fmt.Errorf("hub key: %w", err)
	}
	return nil
}

// MigrateKeyToTPM seals the existing hub.key with the TPM into hub.key.tpm
// and removes hub.key (overwritten first). The hub identity stays the same.
func MigrateKeyToTPM(dir string) (crypto.Keypair, error) {
	return migrateKeyToTPM(dir, tpmkey.Open)
}

func migrateKeyToTPM(dir string, open tpmOpener) (crypto.Keypair, error) {
	sealed, plain := filepath.Join(dir, TPMKeyFile), filepath.Join(dir, KeyFile)
	if _, err := os.Stat(sealed); err == nil {
		return crypto.Keypair{}, errors.New("hub key is already sealed by the TPM")
	}
	kp, err := crypto.LoadKeypair(plain)
	if err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	t, err := open()
	if err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	defer func() { _ = t.Close() }()
	if err := sealKey(t, sealed, kp); err != nil {
		return crypto.Keypair{}, err
	}
	if err := wipe(plain); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key sealed, but %s not removed: %w", KeyFile, err)
	}
	return kp, nil
}

// wipe overwrites a small file with zeros before removing it.
func wipe(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, make([]byte, fi.Size()), 0o600); err != nil {
		return err
	}
	return os.Remove(path)
}

func loadOrCreateKeyFile(dir, file string) (crypto.Keypair, error) {
	path := filepath.Join(dir, file)
	kp, err := crypto.LoadKeypair(path)
	if err == nil {
		return kp, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	if kp, err = crypto.GenerateKeypair(); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub state dir: %w", err)
	}
	if err := crypto.SaveKeypair(path, kp); err != nil {
		return crypto.Keypair{}, fmt.Errorf("hub key: %w", err)
	}
	return kp, nil
}

// SaveEnrollment writes e to dir (atomic rename).
func SaveEnrollment(dir string, e Enrollment) error {
	e.Pending = false
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("save enrollment: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".server-*.tmp")
	if err != nil {
		return fmt.Errorf("save enrollment: %w", err)
	}
	_, err = tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, EnrollmentFile))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save enrollment: %w", err)
	}
	return nil
}

// LoadEnrollment reads the enrollment saved in dir.
func LoadEnrollment(dir string) (Enrollment, error) {
	b, err := os.ReadFile(filepath.Join(dir, EnrollmentFile))
	if err != nil {
		return Enrollment{}, fmt.Errorf("load enrollment (run xs-hubctl enroll first): %w", err)
	}
	var e Enrollment
	if err := json.Unmarshal(b, &e); err != nil {
		return Enrollment{}, fmt.Errorf("parse enrollment: %w", err)
	}
	if _, err := crypto.ParseKey(e.ServerKey); err != nil || len(e.Endpoints) == 0 || e.TunnelIP == "" {
		return Enrollment{}, errors.New("parse enrollment: missing server key, endpoints or tunnel ip")
	}
	if e.pending() {
		if _, err := enroll.ParseToken(e.Token); err != nil {
			return Enrollment{}, fmt.Errorf("parse enrollment: %w", err)
		}
	}
	e.Pending = false
	return e, nil
}
