//go:build !linux && !windows

package update

import (
	"context"
	"errors"
	"os/exec"
)

var errUnsupported = errors.New("hub updates are installed on Linux and Windows only")

// Elevated is false: there is no installer for this platform.
func Elevated() bool { return false }

func install(context.Context, string, Options) error { return errUnsupported }

func hideWindow(*exec.Cmd) {}

// RunElevated is not supported on this platform.
func RunElevated(context.Context, string, ...string) error { return errUnsupported }

const lockFile = ""

func lock(string) (func(), error) { return func() {}, nil }
