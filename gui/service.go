package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// xs-hubd service control (Linux, phase 12): the GUI starts the service when
// its socket is missing and asks on window close whether to stop it. The
// polkit rule from install.sh lets group crossstream start/stop/restart
// xs-hubd.service without a password.
const (
	closeFile     = "gui-close.json"
	startWait     = 45 * time.Second // a stop in progress + Mobile Hotspot (~10 s) on Windows
	stopTimeout   = 30 * time.Second
	EventCloseAsk = "close-ask" // frontend event: show the close dialog

	CloseAsk  = "ask"  // ask every time (default)
	CloseKeep = "keep" // leave the hub running in the background
	CloseStop = "stop" // stop the hub and close
)

// service starts/stops xs-hubd (systemctl or sc.exe) and keeps the close preference.
type service struct {
	path    string // close preference file ("" = not saved)
	run     func(ctx context.Context, args ...string) error
	enabled bool // Linux and Windows

	mu       sync.Mutex
	onClose  string
	quitting bool
}

type closePrefs struct {
	OnClose string `json:"on_close"`
}

func newService() *service {
	s := &service{onClose: CloseAsk, enabled: goruntime.GOOS == "linux" || goruntime.GOOS == "windows", run: serviceCtl}
	if dir, err := os.UserConfigDir(); err == nil {
		s.path = filepath.Join(dir, "crossstream", closeFile)
		var p closePrefs
		if b, err := os.ReadFile(s.path); err == nil && json.Unmarshal(b, &p) == nil && validClose(p.OnClose) {
			s.onClose = p.OnClose
		}
	}
	return s
}

func validClose(s string) bool { return s == CloseAsk || s == CloseKeep || s == CloseStop }

// ensure starts xs-hubd when nothing answers on its socket and waits for
// it; nil when it answers (or service control is off). A killed xs-hubd
// leaves its socket file behind, so the file alone does not count.
func (s *service) ensure(ctx context.Context, sock string) error {
	if !s.enabled || answers(sock) {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	if err := s.run(sctx, "start", serviceName); err != nil {
		return err
	}
	for sctx.Err() == nil {
		if answers(sock) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("xs-hubd started, but its socket %s did not appear", sock)
}

// answers reports whether xs-hubd accepts connections on sock.
func answers(sock string) bool {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// startHub starts xs-hubd through the service manager (polkit / sc.exe)
// and wakes the status loop; while it runs the view says "starting" instead
// of showing the manual commands. One start at a time.
func (a *App) startHub(ctx context.Context) {
	if !a.svc.enabled || !a.starting.CompareAndSwap(false, true) {
		return
	}
	a.mu.Lock()
	v := a.last
	a.mu.Unlock()
	if v.State != stateOK {
		v.Starting = true
		a.publish(ctx, v)
	}
	err := a.svc.ensure(ctx, a.sock)
	msg := ""
	if err != nil {
		a.log.Warn("start xs-hubd", "err", err)
		msg = err.Error()
	}
	a.mu.Lock()
	a.startErr = msg
	a.mu.Unlock()
	a.starting.Store(false)
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// StartHub starts xs-hubd from the GUI ("Uruchom usługę").
func (a *App) StartHub() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		go a.startHub(ctx)
	}
}

// OnClose returns the remembered close choice (ask, keep or stop).
func (s *service) OnClose() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onClose
}

// SetOnClose saves the close choice.
func (s *service) SetOnClose(v string) error {
	if !validClose(v) {
		return fmt.Errorf("close choice %q (want ask, keep or stop)", v)
	}
	s.mu.Lock()
	s.onClose = v
	path := s.path
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(closePrefs{OnClose: v}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("close choice: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("close choice: %w", err)
	}
	return nil
}

// decide is the window-close decision: prevent = keep the window open (the
// frontend asks), stop = stop xs-hubd before closing.
func (s *service) decide(hubRunning bool) (prevent, stop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quitting || !s.enabled || !hubRunning {
		return false, false
	}
	switch s.onClose {
	case CloseKeep:
		return false, false
	case CloseStop:
		return false, true
	}
	return true, false
}

// beforeClose is Wails OnBeforeClose: true keeps the window open.
func (a *App) beforeClose(ctx context.Context) bool {
	prevent, stop := a.svc.decide(a.Current().State == stateOK)
	if stop {
		a.stopHub(ctx)
	}
	if prevent {
		runtime.EventsEmit(ctx, EventCloseAsk)
	}
	return prevent
}

func (a *App) stopHub(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	if err := a.svc.run(sctx, "stop", serviceName); err != nil {
		a.log.Warn("stop xs-hubd", "err", err)
	}
}

// CloseChoice is the answer of the close dialog: keep (leave the hub
// running) or stop (stop xs-hubd); remember saves it. Cancel never calls
// this. The window then closes.
func (a *App) CloseChoice(choice string, remember bool) error {
	if choice != CloseKeep && choice != CloseStop {
		return fmt.Errorf("close choice %q", choice)
	}
	if remember {
		if err := a.svc.SetOnClose(choice); err != nil {
			return err
		}
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if choice == CloseStop {
		a.stopHub(ctx)
	}
	a.svc.mu.Lock()
	a.svc.quitting = true
	a.svc.mu.Unlock()
	runtime.Quit(ctx)
	return nil
}

// ClosePref returns the remembered close choice for Settings.
func (a *App) ClosePref() string { return a.svc.OnClose() }

// SetClosePref changes the remembered close choice (ask, keep, stop).
func (a *App) SetClosePref(v string) error { return a.svc.SetOnClose(v) }

// ServiceControl reports whether the GUI controls xs-hubd (Linux).
func (a *App) ServiceControl() bool { return a.svc.enabled }
