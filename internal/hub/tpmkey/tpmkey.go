// Package tpmkey seals the hub's static private key with a TPM 2.0 (phase
// 14): the key file holds only a blob that this machine's TPM can unseal, so
// a copied disk or state directory does not leak the hub identity.
package tpmkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// DevicePath is the kernel's TPM resource manager device.
const DevicePath = "/dev/tpmrm0"

// ErrNoTPM is returned by Open when the machine has no usable TPM.
var ErrNoTPM = errors.New("tpmkey: no TPM")

// Blob is a sealed secret: the TPM-wrapped private part and the public
// part of a keyed-hash object under the owner storage root key (SRK). The
// SRK is re-derived from its standard template, so nothing persists in the TPM.
type Blob struct {
	Public  []byte `json:"public"`
	Private []byte `json:"private"`
}

// Seal stores secret (at most 128 bytes) in a new sealed object. The secret
// travels to the TPM encrypted by a session salted with the SRK.
func Seal(tpm transport.TPM, secret []byte) (Blob, error) {
	srk, err := createSRK(tpm)
	if err != nil {
		return Blob{}, err
	}
	defer flush(tpm, srk.ObjectHandle)
	sess, err := srkSession(srk, tpm2.AESEncryption(128, tpm2.EncryptIn))
	if err != nil {
		return Blob{}, err
	}
	rsp, err := tpm2.Create{
		ParentHandle: tpm2.AuthHandle{Handle: srk.ObjectHandle, Name: srk.Name, Auth: sess},
		InSensitive: tpm2.TPM2BSensitiveCreate{Sensitive: &tpm2.TPMSSensitiveCreate{
			Data: tpm2.NewTPMUSensitiveCreate(&tpm2.TPM2BSensitiveData{Buffer: secret}),
		}},
		InPublic: tpm2.New2B(tpm2.TPMTPublic{
			Type:    tpm2.TPMAlgKeyedHash,
			NameAlg: tpm2.TPMAlgSHA256,
			ObjectAttributes: tpm2.TPMAObject{
				FixedTPM: true, FixedParent: true, UserWithAuth: true, NoDA: true,
			},
		}),
	}.Execute(tpm)
	if err != nil {
		return Blob{}, fmt.Errorf("tpmkey: seal: %w", err)
	}
	return Blob{Public: tpm2.Marshal(rsp.OutPublic), Private: tpm2.Marshal(rsp.OutPrivate)}, nil
}

// Unseal returns the secret in b. Only the TPM that sealed it can do this.
func Unseal(tpm transport.TPM, b Blob) ([]byte, error) {
	pub, err := tpm2.Unmarshal[tpm2.TPM2BPublic](b.Public)
	if err != nil {
		return nil, fmt.Errorf("tpmkey: blob public: %w", err)
	}
	priv, err := tpm2.Unmarshal[tpm2.TPM2BPrivate](b.Private)
	if err != nil {
		return nil, fmt.Errorf("tpmkey: blob private: %w", err)
	}
	srk, err := createSRK(tpm)
	if err != nil {
		return nil, err
	}
	defer flush(tpm, srk.ObjectHandle)
	loaded, err := tpm2.Load{
		ParentHandle: tpm2.AuthHandle{Handle: srk.ObjectHandle, Name: srk.Name, Auth: tpm2.PasswordAuth(nil)},
		InPrivate:    *priv,
		InPublic:     *pub,
	}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("tpmkey: load (sealed by another TPM?): %w", err)
	}
	defer flush(tpm, loaded.ObjectHandle)
	sess, err := srkSession(srk, tpm2.AESEncryption(128, tpm2.EncryptOut))
	if err != nil {
		return nil, err
	}
	out, err := tpm2.Unseal{
		ItemHandle: tpm2.AuthHandle{Handle: loaded.ObjectHandle, Name: loaded.Name, Auth: sess},
	}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("tpmkey: unseal: %w", err)
	}
	return out.OutData.Buffer, nil
}

func createSRK(tpm transport.TPM) (*tpm2.CreatePrimaryResponse, error) {
	rsp, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("tpmkey: create SRK: %w", err)
	}
	return rsp, nil
}

// srkSession is an HMAC session salted with the SRK that encrypts the
// sensitive parameter as enc says (the secret never crosses the bus in
// clear).
func srkSession(srk *tpm2.CreatePrimaryResponse, enc tpm2.AuthOption) (tpm2.Session, error) {
	pub, err := srk.OutPublic.Contents()
	if err != nil {
		return nil, fmt.Errorf("tpmkey: SRK public: %w", err)
	}
	return tpm2.HMAC(tpm2.TPMAlgSHA256, 16, enc, tpm2.Salted(srk.ObjectHandle, *pub)), nil
}

func flush(tpm transport.TPM, h tpm2.TPMHandle) {
	_, _ = tpm2.FlushContext{FlushHandle: h}.Execute(tpm)
}

type fileFormat struct {
	V       int    `json:"v"`
	Public  string `json:"public"`
	Private string `json:"private"`
}

// Save writes b to path (mode 0600, atomic rename).
func Save(path string, b Blob) (err error) {
	data, err := json.Marshal(fileFormat{V: 1, Public: base64.StdEncoding.EncodeToString(b.Public),
		Private: base64.StdEncoding.EncodeToString(b.Private)})
	if err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tpmkey-*")
	if err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("tpmkey: save: %w", err)
	}
	return nil
}

// Load reads a blob written by Save.
func Load(path string) (Blob, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Blob{}, fmt.Errorf("tpmkey: load: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil || f.V != 1 {
		return Blob{}, fmt.Errorf("tpmkey: load %s: not a sealed key file", filepath.Base(path))
	}
	var b Blob
	if b.Public, err = base64.StdEncoding.DecodeString(f.Public); err == nil {
		b.Private, err = base64.StdEncoding.DecodeString(f.Private)
	}
	if err != nil || len(b.Public) == 0 || len(b.Private) == 0 {
		return Blob{}, fmt.Errorf("tpmkey: load %s: bad blob", filepath.Base(path))
	}
	return b, nil
}
