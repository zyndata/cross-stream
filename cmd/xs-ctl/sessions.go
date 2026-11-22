package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// sessionsCmd prints the session table once, or every -interval with
// -watch until Ctrl-C.
func sessionsCmd(c admin.Client, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "JSON output (with -watch: one line per refresh)")
	watch := fs.Bool("watch", false, "refresh until Ctrl-C")
	interval := fs.Duration("interval", time.Second, "refresh interval with -watch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *interval < 100*time.Millisecond {
		return fmt.Errorf("usage: xs-ctl sessions [-json] [-watch [-interval 1s]]")
	}
	fetch := func(ctx context.Context) ([]admin.Session, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return c.Sessions(ctx)
	}
	if !*watch {
		return showSessions(context.Background(), fetch, stdout, *asJSON, false)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return watchSessions(ctx, fetch, stdout, *interval, *asJSON, 0)
}

type sessionsFetch func(context.Context) ([]admin.Session, error)

// watchSessions refreshes the table every interval until ctx ends (or after
// n refreshes when n > 0). A failed refresh is shown and retried.
func watchSessions(ctx context.Context, fetch sessionsFetch, w io.Writer, interval time.Duration, asJSON bool, n int) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for i := 0; n == 0 || i < n; i++ {
		if err := showSessions(ctx, fetch, w, asJSON, true); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			_, _ = fmt.Fprintln(w, "xs-ctl:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
	return nil
}

func showSessions(ctx context.Context, fetch sessionsFetch, w io.Writer, asJSON, watch bool) error {
	ss, err := fetch(ctx)
	if err != nil {
		return err
	}
	switch {
	case asJSON && watch:
		return json.NewEncoder(w).Encode(ss) // one line per refresh
	case asJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ss)
	}
	now := time.Now()
	if watch {
		_, _ = fmt.Fprintf(w, "\033[H\033[2J%s  (Ctrl-C stops)\n", now.Format("15:04:05"))
	}
	printSessions(w, ss, now)
	return nil
}
