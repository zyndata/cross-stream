package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/beeep"
)

// Typed hub events (internal/hub/events.go) and the notification rules
// (architecture.md "Desktop notifications").
const (
	eventNotice = "events.event"
	notifyGap   = 5 * time.Minute // at most one notification per type per relay
	notifyFile  = "gui-notify.json"
)

// hubEvent mirrors hub.Event.
type hubEvent struct {
	Type   string    `json:"type"`
	At     time.Time `json:"at"`
	Relay  string    `json:"relay"`
	Name   string    `json:"name"`
	Reason string    `json:"reason"`
	Mode   string    `json:"mode"`
	Value  int       `json:"value"`
	Up     int       `json:"up"`
}

// notifyTypes are the event types with a toggle, in Settings order.
var notifyTypes = []string{"relay_left", "relay_joined", "cap_80", "cap_100", "battery_low", "mode_changed",
	"weak_wifi", "speedtest_done"}

// NotifyPrefs are the per-type toggles (missing = on).
type NotifyPrefs map[string]bool

// notifier turns hub events into OS notifications.
type notifier struct {
	path string // prefs file ("" = not saved)
	tr   *texts
	send func(title, body string) error

	mu    sync.Mutex
	prefs NotifyPrefs
	last  map[string]time.Time // type/relay → last notification
}

func newNotifier(tr *texts) *notifier {
	n := &notifier{tr: tr, prefs: NotifyPrefs{}, last: make(map[string]time.Time),
		send: func(title, body string) error { return beeep.Notify(title, body, "") }}
	if dir, err := os.UserConfigDir(); err == nil {
		n.path = filepath.Join(dir, "crossstream", notifyFile)
		if b, err := os.ReadFile(n.path); err == nil {
			_ = json.Unmarshal(b, &n.prefs)
		}
	}
	return n
}

// Prefs returns every toggle.
func (n *notifier) Prefs() NotifyPrefs {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := NotifyPrefs{}
	for _, t := range notifyTypes {
		on, ok := n.prefs[t]
		out[t] = on || !ok
	}
	return out
}

// SetPrefs saves the toggles.
func (n *notifier) SetPrefs(p NotifyPrefs) error {
	n.mu.Lock()
	n.prefs = NotifyPrefs{}
	for _, t := range notifyTypes {
		if on, ok := p[t]; ok {
			n.prefs[t] = on
		}
	}
	b, _ := json.MarshalIndent(n.prefs, "", "  ")
	path := n.path
	n.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	return nil
}

// allow applies the toggle and the per type per relay rate limit.
func (n *notifier) allow(e hubEvent, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if on, ok := n.prefs[e.Type]; ok && !on {
		return false
	}
	k := e.Type + "/" + e.Relay
	if e.Relay == "" { // hub-wide events: only a repeat of the same message is noise
		_, body := eventText(n.tr, e)
		k += body
	}
	if t, ok := n.last[k]; ok && now.Sub(t) < notifyGap {
		return false
	}
	n.last[k] = now
	return true
}

// handle shows e when allowed; it reports whether a notification was sent.
func (n *notifier) handle(e hubEvent, now time.Time) bool {
	title, body := eventText(n.tr, e)
	if title == "" || !n.allow(e, now) {
		return false
	}
	return n.send(title, body) == nil
}

// eventText is the notification for e in the active language ("" = no notification).
func eventText(tr *texts, e hubEvent) (title, body string) {
	if strings.HasPrefix(e.Relay, "u") { // a local link of the hub (phase 12)
		switch e.Type {
		case "relay_joined":
			return "CrossStream: " + e.Name, tr.T("go.notify.localJoined", nil)
		case "relay_left":
			return "CrossStream: " + e.Name, tr.T("go.notify.localLeft", map[string]any{"reason": localReason(tr, e.Reason)})
		}
	}
	who := e.Name
	if who == "" {
		who = tr.T("go.notify.phone", nil)
	}
	switch e.Type {
	case "relay_joined":
		return "CrossStream: " + who, tr.T("go.notify.joined", nil)
	case "relay_left":
		return "CrossStream: " + who, tr.T("go.notify.left", map[string]any{"reason": leftReason(tr, e.Reason)})
	case "cap_80":
		return "CrossStream: " + who, tr.T("go.notify.cap80", map[string]any{"mb": e.Value})
	case "cap_100":
		return "CrossStream: " + who, tr.T("go.notify.cap100", map[string]any{"mb": e.Value})
	case "battery_low":
		return "CrossStream: " + who, tr.T("go.notify.battery", map[string]any{"pct": e.Value})
	case "weak_wifi":
		return "CrossStream: " + who, tr.T("go.notify.weakWifi", map[string]any{"dbm": e.Value})
	case "mode_changed":
		m := e.Mode
		if k := "go.mode." + e.Mode; tr.T(k, nil) != k {
			m = tr.T(k, nil)
		}
		return "CrossStream", tr.T("go.notify.mode", map[string]any{"mode": m})
	case "speedtest_done":
		return "CrossStream", tr.T("go.notify.speed", map[string]any{"result": speedText(tr, e.Value, e.Up)})
	}
	return "", ""
}

// speedText names the measured directions: an upload-only test has no
// download figure (it said "0 Mb/s pobierania", phase 18).
func speedText(tr *texts, down, up int) string {
	p := map[string]any{"down": down, "up": up}
	switch {
	case down > 0 && up > 0:
		return tr.T("go.speed.both", p)
	case up > 0:
		return tr.T("go.speed.up", p)
	case down > 0:
		return tr.T("go.speed.down", p)
	}
	return tr.T("go.speed.none", nil)
}

func leftReason(tr *texts, r string) string {
	switch r {
	case "leaving: limit":
		return tr.T("go.left.limit", nil)
	case "leaving: battery":
		return tr.T("go.left.battery", nil)
	case "leaving: user":
		return tr.T("go.left.user", nil)
	case "leaving: no_cellular", "leaving: no_data":
		return tr.T("go.left.noData", nil)
	case "":
		return ""
	}
	return tr.T("go.left.lost", nil)
}

func localReason(tr *texts, r string) string {
	switch r {
	case "leaving: limit":
		return tr.T("go.left.limit", nil)
	case "leaving: off":
		return tr.T("go.local.off", nil)
	case "leaving: absent":
		return tr.T("go.local.absent", nil)
	case "connection lost":
		return tr.T("go.local.noServer", nil)
	}
	return ""
}
