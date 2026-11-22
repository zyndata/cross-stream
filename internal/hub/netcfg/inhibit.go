package netcfg

import (
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Inhibitor holds a logind inhibitor lock (sleep + lid switch) while on, via
// `systemd-inhibit ... cat`: the lock lives as long as cat's stdin pipe, so
// closing it (or xs-hubd dying) releases the lock.
type Inhibitor struct {
	Why     string
	Command []string // nil = systemd-inhibit; tests use a plain command

	mu    sync.Mutex
	stdin io.WriteCloser
	done  chan struct{}
}

func (i *Inhibitor) command() []string {
	if i.Command != nil {
		return i.Command
	}
	return []string{"systemd-inhibit", "--what=sleep:handle-lid-switch", "--who=CrossStream",
		"--why=" + i.Why, "--mode=block", "cat"}
}

// Held reports whether the lock is held (the helper still runs).
func (i *Inhibitor) Held() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.heldLocked()
}

func (i *Inhibitor) heldLocked() bool {
	if i.done == nil {
		return false
	}
	select {
	case <-i.done:
		return false
	default:
		return true
	}
}

// Set takes (on) or releases (off) the lock. Taking it again after the
// helper exited (e.g. logind restarted) starts a new one.
func (i *Inhibitor) Set(on bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !on {
		i.releaseLocked()
		return nil
	}
	if i.heldLocked() {
		return nil
	}
	i.releaseLocked()
	argv := i.command()
	cmd := exec.Command(argv[0], argv[1:]...)
	w, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("inhibit: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("inhibit: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	i.stdin, i.done = w, done
	return nil
}

func (i *Inhibitor) releaseLocked() {
	if i.stdin != nil {
		_ = i.stdin.Close()
		<-i.done
	}
	i.stdin, i.done = nil, nil
}
