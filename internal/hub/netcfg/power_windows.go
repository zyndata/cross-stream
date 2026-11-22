package netcfg

import (
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// SetThreadExecutionState flags (winbase.h).
const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// PowerRequest keeps Windows from sleeping while the hub serves clients
// (SetThreadExecutionState on a dedicated OS thread, which must stay alive
// for the request to hold). Closing the lid still follows the power plan.
type PowerRequest struct {
	mu   sync.Mutex
	held bool
	req  chan bool
	errs chan error
}

// Held reports whether the request is active.
func (p *PowerRequest) Held() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

// Set takes (on) or releases the request.
func (p *PowerRequest) Set(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on == p.held {
		return nil
	}
	if p.req == nil {
		p.req, p.errs = make(chan bool), make(chan error)
		go p.loop()
	}
	p.req <- on
	if err := <-p.errs; err != nil {
		return err
	}
	p.held = on
	return nil
}

func (p *PowerRequest) loop() {
	runtime.LockOSThread() // the state belongs to this thread
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	for on := range p.req {
		flags := uintptr(esContinuous)
		if on {
			flags |= esSystemRequired
		}
		r, _, err := proc.Call(flags)
		if r == 0 {
			p.errs <- fmt.Errorf("SetThreadExecutionState: %w", err)
			continue
		}
		p.errs <- nil
	}
}
