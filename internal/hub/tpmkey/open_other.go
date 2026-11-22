//go:build !linux

package tpmkey

import "github.com/google/go-tpm/tpm2/transport"

// Open reports ErrNoTPM: TPM sealing is Linux-only for now (the Windows hub
// in phase 16 keeps the 0600-style file).
func Open() (transport.TPMCloser, error) { return nil, ErrNoTPM }
