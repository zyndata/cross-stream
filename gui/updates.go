package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zyndata/cross-stream/internal/update"
)

const (
	updateFile = "gui-update.json"
	// EventUpdate is the frontend event carrying an UpdateView.
	EventUpdate = "update"
)

// Update states shown by the frontend.
const (
	updIdle        = "idle"
	updChecking    = "checking"
	updDownloading = "downloading"
	updInstalling  = "installing"
)

// UpdateOffer is a newer release.
type UpdateOffer struct {
	Tag  string `json:"tag"`
	Page string `json:"page"`
	Size int64  `json:"size"` // hub archive for this platform, 0 = none
}

// UpdateView is the update state for the banner and the Settings card.
type UpdateView struct {
	Version    string       `json:"version"`
	Release    bool         `json:"release"` // a release build (development builds are never updated)
	Auto       bool         `json:"auto"`    // check when the app starts
	State      string       `json:"state"`
	Checked    bool         `json:"checked"` // a check succeeded in this session
	Latest     string       `json:"latest"`
	Available  *UpdateOffer `json:"available"`
	Dismissed  bool         `json:"dismissed"` // the banner was closed for this version
	Progress   int          `json:"progress"`  // percent while downloading
	Error      string       `json:"error"`
	CanInstall bool         `json:"can_install"`
}

type updatePrefs struct {
	Auto      *bool  `json:"auto,omitempty"`
	Dismissed string `json:"dismissed,omitempty"`
}

// updater checks for a newer release when the app starts (unless turned off)
// and on request, and installs it through xs-hubctl update -apply, elevated.
type updater struct {
	version string
	tr      *texts // language of the messages in UpdateView.Error
	exe     string // this executable
	path    string // prefs file ("" = not saved)
	client  *update.Client
	// elevate runs xs-hubctl as root / Administrator (pkexec, UAC); tests replace it.
	elevate func(ctx context.Context, exe string, args ...string) error
	// startRetry: pauses before the next try when the check at start found
	// no network (the GUI starts with the session, often before Wi-Fi).
	startRetry []time.Duration

	mu      sync.Mutex
	prefs   updatePrefs
	view    UpdateView
	release update.Release
}

func newUpdater(version string, tr *texts) *updater {
	u := &updater{version: version, tr: tr, client: update.NewClient(version), elevate: update.RunElevated,
		startRetry: []time.Duration{30 * time.Second, 2 * time.Minute}}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		u.exe = exe
	}
	if dir, err := os.UserConfigDir(); err == nil {
		u.path = filepath.Join(dir, "crossstream", updateFile)
		u.prefs = loadUpdatePrefs(u.path)
	}
	_, release := update.ParseVersion(version)
	u.view = UpdateView{Version: version, Release: release, State: updIdle}
	return u
}

// loadUpdatePrefs reads the saved preferences: the defaults when there are
// none or the file is damaged (a half-read file could turn the check off).
func loadUpdatePrefs(path string) updatePrefs {
	var p updatePrefs
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &p) != nil {
		return updatePrefs{}
	}
	return p
}

// hubctl returns xs-hubctl and the GUI prefix for -apply; "" when this GUI
// cannot install (a build tree, or a Linux GUI outside a bin directory).
func (u *updater) hubctl() (ctl, prefix string) {
	if u.exe == "" {
		return "", ""
	}
	ctl = update.Hubctl(u.exe)
	switch goruntime.GOOS {
	case "linux":
		prefix = update.GUIPrefix(u.exe)
		if prefix == "" {
			return "", ""
		}
	case "windows":
	default:
		return "", ""
	}
	return ctl, prefix
}

// View returns the current state.
func (u *updater) View() UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.viewLocked()
}

func (u *updater) viewLocked() UpdateView {
	v := u.view
	v.Auto = u.prefs.Auto == nil || *u.prefs.Auto
	v.Dismissed = v.Available != nil && v.Available.Tag == u.prefs.Dismissed
	ctl, _ := u.hubctl()
	v.CanInstall = v.Available != nil && v.Available.Size > 0 && ctl != ""
	return v
}

func (u *updater) set(f func(v *UpdateView)) UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(&u.view)
	return u.viewLocked()
}

// Check asks GitHub for the latest release.
func (u *updater) Check(ctx context.Context) (UpdateView, error) {
	u.mu.Lock()
	if u.view.State != updIdle {
		v := u.viewLocked()
		u.mu.Unlock()
		return v, nil
	}
	u.view.State = updChecking
	u.mu.Unlock()
	r, err := u.client.Latest(ctx)
	if err != nil {
		msg := u.tr.T("go.upd.checkFailed", nil)
		switch {
		case errors.Is(err, update.ErrNoRelease):
			msg = u.tr.T("go.upd.noRelease", nil)
		case errors.Is(err, update.ErrRateLimited):
			msg = u.tr.T("go.upd.rateLimited", nil)
		}
		v := u.set(func(v *UpdateView) { v.State, v.Error = updIdle, msg })
		return v, fmt.Errorf("check for updates: %w", err)
	}
	v := u.set(func(v *UpdateView) {
		v.State, v.Error, v.Checked, v.Latest, v.Available = updIdle, "", true, r.Tag, nil
		if r.Newer(u.version) {
			a, _ := r.HubArchive(goruntime.GOOS, goruntime.GOARCH)
			v.Available = &UpdateOffer{Tag: r.Tag, Page: r.Page, Size: a.Size}
		}
	})
	u.mu.Lock()
	u.release = r
	u.mu.Unlock()
	return v, nil
}

// SetAuto turns the check at start on or off.
func (u *updater) SetAuto(on bool) error {
	u.mu.Lock()
	u.prefs.Auto = &on
	u.mu.Unlock()
	return u.save()
}

// Dismiss hides the banner for the offered version (a newer one shows again).
func (u *updater) Dismiss() error {
	u.mu.Lock()
	if u.view.Available != nil {
		u.prefs.Dismissed = u.view.Available.Tag
	}
	u.mu.Unlock()
	return u.save()
}

func (u *updater) save() error {
	u.mu.Lock()
	b, _ := json.MarshalIndent(u.prefs, "", "  ")
	path := u.path
	u.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}

// install downloads the hub archive of the offered release, checks it against
// SHA256SUMS and runs xs-hubctl update -apply elevated (pkexec / UAC). True
// when it was installed and the new GUI should be started.
func (u *updater) install(ctx context.Context, emit func(UpdateView)) bool {
	u.mu.Lock()
	r := u.release
	ok := u.view.State == updIdle && u.view.Available != nil
	if ok {
		u.view.State, u.view.Progress, u.view.Error = updDownloading, 0, ""
	}
	v := u.viewLocked()
	u.mu.Unlock()
	if !ok {
		return false
	}
	emit(v)
	fail := func(msg string) bool {
		emit(u.set(func(v *UpdateView) { v.State, v.Error = updIdle, msg }))
		return false
	}
	ctl, prefix := u.hubctl()
	a, found := r.HubArchive(goruntime.GOOS, goruntime.GOARCH)
	if ctl == "" || !found {
		return fail(u.tr.T("go.upd.cannotInstall", nil))
	}
	want, err := u.client.Sum(ctx, r, a.Name)
	if err != nil {
		return fail(u.tr.T("go.upd.sumFailed", map[string]any{"error": err.Error()}))
	}
	dir, err := os.MkdirTemp("", "crossstream-update-")
	if err != nil {
		return fail(err.Error())
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, err := u.client.Download(ctx, a, want, dir, func(done, total int64) {
		if total > 0 {
			emit(u.set(func(v *UpdateView) { v.Progress = int(done * 100 / total) }))
		}
	})
	if errors.Is(err, update.ErrStalled) {
		return fail(u.tr.T("go.upd.stalled", nil))
	}
	if err != nil {
		return fail(u.tr.T("go.upd.downloadFailed", map[string]any{"error": err.Error()}))
	}
	emit(u.set(func(v *UpdateView) { v.State, v.Progress = updInstalling, 100 }))
	logPath := filepath.Join(dir, "install.log")
	if prefix == "" {
		prefix = "none"
	}
	err = u.elevate(ctx, ctl, "update", "-apply", path, "-sha256", want, "-gui-prefix", prefix, "-log", logPath)
	if errors.Is(err, update.ErrCanceled) {
		return fail(u.tr.T("go.upd.canceled", nil))
	}
	if err != nil {
		msg := err.Error()
		if b, rerr := os.ReadFile(logPath); rerr == nil && len(strings.TrimSpace(string(b))) > 0 {
			msg = lastLines(string(b), 3)
		}
		if strings.Contains(msg, update.ErrBusy.Error()) {
			return fail(u.tr.T("go.upd.busy", nil))
		}
		return fail(u.tr.T("go.upd.installFailed", map[string]any{"error": msg}))
	}
	return true
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

// UpdateInfo returns the update state (Settings card, banner).
func (a *App) UpdateInfo() UpdateView { return a.upd.View() }

// CheckUpdate checks for a newer release now; a failure is in the view's
// Error (UI language), the cause in the log.
func (a *App) CheckUpdate() (UpdateView, error) {
	v, err := a.upd.Check(a.context())
	if err != nil {
		a.log.Info("update check", "err", err)
	}
	a.emitUpdate(v)
	return v, nil
}

// SetUpdateAuto turns the check at start on or off.
func (a *App) SetUpdateAuto(on bool) error { return a.upd.SetAuto(on) }

// DismissUpdate hides the banner for the offered version.
func (a *App) DismissUpdate() error {
	err := a.upd.Dismiss()
	a.emitUpdate(a.upd.View())
	return err
}

// InstallUpdate downloads and installs the offered release in the background
// (progress arrives as "update" events), then starts the new GUI and quits.
func (a *App) InstallUpdate() error {
	v := a.upd.View()
	if !v.CanInstall || v.State != updIdle {
		return errors.New("error: " + a.tr.T("go.upd.unavailable", nil))
	}
	ctx := a.context()
	go func() {
		if !a.upd.install(ctx, a.emitUpdate) {
			return
		}
		a.relaunch(ctx)
	}()
	return nil
}

// OpenURL opens a release page in the browser.
func (a *App) OpenURL(url string) {
	if strings.HasPrefix(url, "https://github.com/"+update.Repository+"/") {
		runtime.BrowserOpenURL(a.context(), url)
	}
}

// relaunch starts the GUI just installed at the same path and quits this one
// without the close dialog (xs-hubd keeps running, restarted by the installer).
func (a *App) relaunch(ctx context.Context) {
	if err := exec.Command(a.upd.exe).Start(); err != nil {
		a.log.Warn("start the new GUI", "err", err)
	}
	a.svc.mu.Lock()
	a.svc.quitting = true
	a.svc.mu.Unlock()
	runtime.Quit(ctx)
}

func (a *App) checkAtStart(ctx context.Context) {
	a.upd.checkAtStart(ctx, a.emitUpdate, func(err error) { a.log.Info("update check", "err", err) })
}

// checkAtStart checks once when the app starts, unless turned off or a
// development build. Without network it tries again after each startRetry
// pause; it stops once any check (also "Check now") got an answer.
func (u *updater) checkAtStart(ctx context.Context, emit func(UpdateView), logf func(error)) {
	if v := u.View(); !v.Auto || !v.Release {
		return
	}
	for i := 0; ; i++ {
		_, err := u.Check(ctx)
		if err != nil {
			logf(err)
		}
		emit(u.View())
		if err == nil || errors.Is(err, update.ErrNoRelease) || errors.Is(err, update.ErrRateLimited) || i >= len(u.startRetry) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.startRetry[i]):
		}
		if v := u.View(); v.Checked || !v.Auto {
			return
		}
	}
}

func (a *App) emitUpdate(v UpdateView) {
	if ctx := a.context(); ctx != nil {
		runtime.EventsEmit(ctx, EventUpdate, v)
	}
}

func (a *App) context() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}
