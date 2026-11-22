//go:build tpmsim

package tpmkey

import (
	"bytes"
	"testing"

	"github.com/google/go-tpm/tpm2/transport/simulator"
)

// The simulator (go-tpm-tools, MS reference TPM) needs cgo and OpenSSL
// headers: go test -tags tpmsim (CI step "tpm simulator", see testing.md).

func TestSealUnsealSimulator(t *testing.T) {
	tpm, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{0xA5}, 32)
	b, err := Seal(tpm, secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b.Private, secret) || bytes.Contains(b.Public, secret) {
		t.Fatal("secret visible in the blob")
	}
	got, err := Unseal(tpm, b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("unsealed %x", got)
	}
	bad := Blob{Public: b.Public, Private: append([]byte{}, b.Private...)}
	bad.Private[len(bad.Private)-1] ^= 1
	if _, err := Unseal(tpm, bad); err == nil {
		t.Fatal("tampered blob unsealed")
	}
	if err := tpm.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh simulator has a new owner seed: like another machine's TPM.
	other, err := simulator.OpenSimulator()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := Unseal(other, b); err == nil {
		t.Fatal("another TPM unsealed the blob")
	}
}
