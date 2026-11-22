package netcfg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner executes a command with optional stdin and returns its combined
// output. Tests replace it; nil means exec with LC_ALL=C (stable output).
type Runner func(ctx context.Context, stdin string, name string, args ...string) (string, error)

func execRun(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func runWith(ctx context.Context, r Runner, stdin, name string, args ...string) (string, error) {
	if r == nil {
		r = execRun
	}
	return r(ctx, stdin, name, args...)
}
