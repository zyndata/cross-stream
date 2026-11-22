//go:build !windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// serviceName is the systemd unit of xs-hubd.
const serviceName = "xs-hubd.service"

// serviceCtl runs systemctl without a password prompt (polkit decides).
func serviceCtl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--no-ask-password"}, args...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}
