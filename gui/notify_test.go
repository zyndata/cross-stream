package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifier(t *testing.T) {
	var sent []string
	n := &notifier{tr: testTexts(langPL), path: filepath.Join(t.TempDir(), "n.json"), prefs: NotifyPrefs{}, last: map[string]time.Time{},
		send: func(title, body string) error { sent = append(sent, title+"|"+body); return nil }}
	now := time.Unix(1_000_000, 0)
	left := hubEvent{Type: "relay_left", Relay: "r1", Name: "anna", Reason: "leaving: limit"}
	if !n.handle(left, now) || !strings.Contains(sent[0], "limit danych") {
		t.Fatalf("first: %v", sent)
	}
	if n.handle(left, now.Add(4*time.Minute)) {
		t.Fatal("same type and relay within 5 min shown")
	}
	if !n.handle(hubEvent{Type: "relay_left", Relay: "r2", Name: "bob"}, now.Add(time.Minute)) {
		t.Fatal("another relay rate-limited")
	}
	if !n.handle(left, now.Add(5*time.Minute)) {
		t.Fatal("not shown again after 5 min")
	}
	if n.handle(hubEvent{Type: "unknown"}, now) {
		t.Fatal("unknown type shown")
	}
	if err := n.SetPrefs(NotifyPrefs{"battery_low": false, "bogus": true}); err != nil {
		t.Fatal(err)
	}
	if n.handle(hubEvent{Type: "battery_low", Relay: "r1", Value: 20}, now) {
		t.Fatal("disabled type shown")
	}
	p := n.Prefs()
	if p["battery_low"] || !p["relay_left"] || len(p) != len(notifyTypes) {
		t.Fatalf("prefs %v", p)
	}
}

func TestEventText(t *testing.T) {
	pl := testTexts(langPL)
	for _, typ := range notifyTypes {
		if title, body := eventText(pl, hubEvent{Type: typ, Name: "anna", Mode: "DIRECT", Value: 5}); title == "" || body == "" {
			t.Errorf("%s: no text", typ)
		}
	}
	if _, body := eventText(pl, hubEvent{Type: "mode_changed", Mode: "DIRECT"}); !strings.Contains(body, "bezpośredni") {
		t.Errorf("mode text %q", body)
	}
	for _, tc := range []struct {
		down, up int
		want     string
	}{
		{0, 43, "Test łączy zakończony: wysyłanie 43 Mb/s."},
		{80, 0, "Test łączy zakończony: pobieranie 80 Mb/s."},
		{80, 30, "Test łączy zakończony: pobieranie 80 Mb/s, wysyłanie 30 Mb/s."},
		{0, 0, "Test łączy zakończony: brak wyniku."},
	} {
		if _, body := eventText(pl, hubEvent{Type: "speedtest_done", Value: tc.down, Up: tc.up}); body != tc.want {
			t.Errorf("speed %d/%d: %q", tc.down, tc.up, body)
		}
	}
	if _, body := eventText(pl, hubEvent{Type: "relay_left", Reason: "control lost"}); !strings.Contains(body, "utrata") {
		t.Errorf("left text %q", body)
	}
	if _, body := eventText(pl, hubEvent{Type: "relay_left", Relay: "u2", Name: "Modem", Reason: "leaving: limit"}); body != "Łącze lokalne rozłączone (limit danych wyczerpany)." {
		t.Errorf("local link text %q", body)
	}
}
