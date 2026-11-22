package tpmkey

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// Open connects to the TPM resource manager; ErrNoTPM when there is none.
func Open() (transport.TPMCloser, error) {
	t, err := linuxtpm.Open(DevicePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoTPM
	}
	if err != nil {
		return nil, fmt.Errorf("tpmkey: open %s: %w", DevicePath, err)
	}
	return t, nil
}
