package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/zyndata/cross-stream/internal/update"
)

// updateCmd checks the public repository for a newer release and, with
// -install, downloads the hub archive of this platform, checks it against
// SHA256SUMS and installs it. -apply installs an archive the GUI downloaded
// (the GUI runs it through pkexec / UAC). Nothing checks on its own.
func updateCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	doInstall := fs.Bool("install", false, "download, verify and install the new release (root / Administrator)")
	apply := fs.String("apply", "", "install this downloaded hub archive (with -sha256)")
	sum := fs.String("sha256", "", "expected SHA-256 of the -apply archive")
	guiPrefix := fs.String("gui-prefix", "auto", "Linux: prefix of the installed GUI (auto: found on PATH; none: leave it)")
	logPath := fs.String("log", "", "also write the installer output to this new file (it must not exist)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o := update.Options{GUIPrefix: guiPrefixOf(*guiPrefix), Log: os.Stdout}
	if *logPath != "" {
		// A new file only: -apply runs as root with a path the user chose,
		// so an existing file or a planted symlink is never written through.
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("log: %w", err)
		}
		defer func() { _ = f.Close() }()
		o.Log = io.MultiWriter(os.Stdout, f)
	}
	u := hubUpdate{c: update.NewClient(version), installed: version, out: os.Stdout, elevated: update.Elevated}
	err := u.run(ctx, *apply, *sum, *doInstall, o)
	if err != nil && *logPath != "" {
		_, _ = fmt.Fprintln(o.Log, "error:", err)
	}
	return err
}

// hubUpdate is one xs-hubctl update run (tests swap the client, the output
// and the privilege check).
type hubUpdate struct {
	c         *update.Client
	installed string
	out       io.Writer
	elevated  func() bool
}

func (u hubUpdate) run(ctx context.Context, apply, sum string, doInstall bool, o update.Options) error {
	if apply != "" {
		if err := update.Apply(ctx, apply, sum, o); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(o.Log, "installed")
		return nil
	}
	c, out := u.c, u.out
	r, err := c.Latest(ctx)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	_, _ = fmt.Fprintf(out, "installed %s, latest release %s\n", u.installed, r.Tag)
	if _, ok := update.ParseVersion(u.installed); !ok {
		_, _ = fmt.Fprintln(out, "this is a development build: it is not updated from releases")
		return nil
	}
	if !r.Newer(u.installed) {
		_, _ = fmt.Fprintln(out, "up to date")
		return nil
	}
	a, ok := r.HubArchive(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("%s has no hub archive for %s/%s: %s", r.Tag, runtime.GOOS, runtime.GOARCH, r.Page)
	}
	_, _ = fmt.Fprintf(out, "%s (%.1f MB) %s\n", a.Name, float64(a.Size)/1e6, r.Page)
	if !doInstall {
		_, _ = fmt.Fprintln(out, "install it: sudo xs-hubctl update -install (Windows: from an elevated terminal)")
		return nil
	}
	if !u.elevated() {
		return update.ErrNotElevated
	}
	want, err := c.Sum(ctx, r, a.Name)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "crossstream-update-")
	if err != nil {
		return fmt.Errorf("temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, err := c.Download(ctx, a, want, dir, func(done, total int64) {
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\rdownloading %3d%%", done*100/total)
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if err := update.Apply(ctx, path, want, o); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "installed", r.Tag)
	return nil
}

// guiPrefixOf resolves -gui-prefix: "auto" = the prefix of xs-hub-gui on
// PATH (Linux; Windows installs the GUI with the hub), "none" = no GUI.
func guiPrefixOf(v string) string {
	switch v {
	case "none", "":
		return ""
	case "auto":
		if runtime.GOOS != "linux" {
			return ""
		}
		p, err := exec.LookPath("xs-hub-gui")
		if err != nil {
			return ""
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return update.GUIPrefix(p)
	}
	return v
}
