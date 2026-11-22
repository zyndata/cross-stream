package netcfg

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"unicode/utf16"
)

//go:embed winps/*.ps1
var psScripts embed.FS

// runPS runs an embedded PowerShell script (winps/<name>) with the string
// variables vars set first. The script travels as -EncodedCommand, so no
// argument quoting is involved.
func runPS(ctx context.Context, run Runner, name string, vars map[string]string) (string, error) {
	body, err := psScripts.ReadFile("winps/" + name)
	if err != nil {
		return "", fmt.Errorf("powershell %s: %w", name, err)
	}
	var b strings.Builder
	// UTF-8 output: adapter aliases are localised ("Połączenie lokalne* 10").
	b.WriteString("$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n" +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8\n")
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "$%s = '%s'\n", k, strings.ReplaceAll(vars[k], "'", "''"))
	}
	b.Write(body)
	if run == nil {
		run = psExec
	}
	out, err := run(ctx, "", "powershell.exe", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePS(b.String()))
	if err != nil {
		return out, fmt.Errorf("powershell %s: %w", name, err)
	}
	return out, nil
}

// psExec runs PowerShell; its errors carry the output, not the (long,
// encoded) command line.
func psExec(ctx context.Context, _ string, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%w: %s", err, psError(buf.String()))
	}
	return buf.String(), nil
}

// psError keeps the useful part of a PowerShell error output: the first
// non-empty lines, without the "At line:" position block.
func psError(out string) string {
	var keep []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "At line:") || strings.HasPrefix(ln, "+ ") || ln == "+" {
			continue
		}
		keep = append(keep, ln)
		if len(keep) == 3 {
			break
		}
	}
	return strings.Join(keep, " ")
}

// encodePS is the -EncodedCommand form: base64 of UTF-16LE.
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}
