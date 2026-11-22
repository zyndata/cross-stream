package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceEnsureAndClose(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "hubd.sock")
	// A killed xs-hubd leaves its socket file: that is not a running hub.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	s := &service{path: filepath.Join(dir, "cfg", closeFile), enabled: true, onClose: CloseAsk,
		run: func(_ context.Context, args ...string) error {
			calls = append(calls, args[0])
			if args[0] == "start" { // the daemon listens on its socket
				_ = os.Remove(sock)
				ln, err := net.Listen("unix", sock)
				if err != nil {
					return err
				}
				t.Cleanup(func() { _ = ln.Close() })
			}
			return nil
		}}
	if err := s.ensure(context.Background(), sock); err != nil || len(calls) != 1 || calls[0] != "start" {
		t.Fatalf("stale socket file: %v %v, want one start", err, calls)
	}
	if err := s.ensure(context.Background(), sock); err != nil || len(calls) != 1 {
		t.Fatalf("hub answers: started again %v", calls)
	}
	s.run = func(context.Context, ...string) error { return errors.New("polkit: not authorized") }
	if err := s.ensure(context.Background(), filepath.Join(dir, "gone.sock")); err == nil {
		t.Fatal("failed start not reported")
	}

	// Close decision.
	if p, st := s.decide(true); !p || st {
		t.Fatal("ask: want the dialog")
	}
	if p, _ := s.decide(false); p {
		t.Fatal("hub not running: close at once")
	}
	if err := s.SetOnClose(CloseStop); err != nil {
		t.Fatal(err)
	}
	if p, st := s.decide(true); p || !st {
		t.Fatal("remembered stop")
	}
	if err := s.SetOnClose("later"); err == nil {
		t.Fatal("bad choice accepted")
	}
	s.quitting = true
	if p, st := s.decide(true); p || st {
		t.Fatal("quitting after the dialog must close")
	}
	// The remembered choice survives a restart.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "x"))
	s2 := &service{path: s.path}
	if err := s2.SetOnClose(CloseKeep); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.path)
	if string(b) == "" || s2.OnClose() != CloseKeep {
		t.Fatalf("saved %q", b)
	}
}
