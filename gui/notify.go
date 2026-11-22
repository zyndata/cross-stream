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
	send func(title, body string) error

	mu    sync.Mutex
	prefs NotifyPrefs
	last  map[string]time.Time // type/relay → last notification
}

func newNotifier() *notifier {
	n := &notifier{prefs: NotifyPrefs{}, last: make(map[string]time.Time),
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
		_, body := eventText(e)
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
	title, body := eventText(e)
	if title == "" || !n.allow(e, now) {
		return false
	}
	return n.send(title, body) == nil
}

// modeNames are the Polish hub mode names.
var modeNames = map[string]string{"BONDED": "połączony przez serwer", "DIRECT": "tryb bezpośredni",
	"CONNECTING": "łączenie", "OFFLINE": "brak telefonów i łączy lokalnych"}

// eventText is the Polish notification for e ("" = no notification).
func eventText(e hubEvent) (title, body string) {
	if strings.HasPrefix(e.Relay, "u") { // a local link of the hub (phase 12)
		switch e.Type {
		case "relay_joined":
			return "CrossStream: " + e.Name, "Łącze lokalne połączone."
		case "relay_left":
			return "CrossStream: " + e.Name, "Łącze lokalne rozłączone" + localReason(e.Reason) + "."
		}
	}
	who := e.Name
	if who == "" {
		who = "Telefon"
	}
	switch e.Type {
	case "relay_joined":
		return "CrossStream: " + who, "Telefon dołączył i udostępnia dane."
	case "relay_left":
		return "CrossStream: " + who, "Telefon odłączył się" + leftReason(e.Reason) + "."
	case "cap_80":
		return "CrossStream: " + who, fmt.Sprintf("Wykorzystano 80%% limitu %d MB.", e.Value)
	case "cap_100":
		return "CrossStream: " + who, fmt.Sprintf("Limit %d MB wyczerpany — telefon nie jest już używany.", e.Value)
	case "battery_low":
		return "CrossStream: " + who, fmt.Sprintf("Słaba bateria telefonu: %d%%.", e.Value)
	case "weak_wifi":
		return "CrossStream: " + who, fmt.Sprintf("Słaby sygnał Wi-Fi do huba (%d dBm) — przybliż telefon.", e.Value)
	case "mode_changed":
		m := modeNames[e.Mode]
		if m == "" {
			m = e.Mode
		}
		return "CrossStream", "Tryb huba: " + m + "."
	case "speedtest_done":
		return "CrossStream", "Test łączy zakończony: " + speedText(e.Value, e.Up) + "."
	}
	return "", ""
}

// speedText names the measured directions: an upload-only test has no
// download figure (it said "0 Mb/s pobierania", phase 18).
func speedText(down, up int) string {
	switch {
	case down > 0 && up > 0:
		return fmt.Sprintf("pobieranie %d Mb/s, wysyłanie %d Mb/s", down, up)
	case up > 0:
		return fmt.Sprintf("wysyłanie %d Mb/s", up)
	case down > 0:
		return fmt.Sprintf("pobieranie %d Mb/s", down)
	}
	return "brak wyniku"
}

func leftReason(r string) string {
	switch r {
	case "leaving: limit":
		return " (limit danych wyczerpany)"
	case "leaving: battery":
		return " (słaba bateria)"
	case "leaving: user":
		return " (wyłączony przez właściciela)"
	case "leaving: no_cellular", "leaving: no_data":
		return " (brak danych komórkowych)"
	case "":
		return ""
	}
	return " (utrata połączenia)"
}

func localReason(r string) string {
	switch r {
	case "leaving: limit":
		return " (limit danych wyczerpany)"
	case "leaving: off":
		return " (wyłączone w ustawieniach)"
	case "leaving: absent":
		return " (odłączone od laptopa)"
	case "connection lost":
		return " (brak połączenia z serwerem)"
	}
	return ""
}
