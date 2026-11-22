package main

import (
	"encoding/json"
	"time"
)

// IPC names of xs-hubd added in phase 11 (internal/hub/rpc_ext.go).
const (
	methodBypassGet = "bypass.get"
	methodBypassSet = "bypass.set"
	methodFwdList   = "forwards.list"
	methodFwdSet    = "forwards.set"
	methodUsageGet  = "usage.get"
)

// The results below go to the frontend as the hub sends them (the GUI does
// not import internal/hub: decisions.md phase 09).

// Bypass returns the bypass list ({domains, cidrs, learned, relays}).
func (a *App) Bypass() (json.RawMessage, error) {
	var r json.RawMessage
	return r, a.call(callTimeout, methodBypassGet, nil, &r)
}

// SetBypass replaces the bypass list.
func (a *App) SetBypass(domains, cidrs []string) (json.RawMessage, error) {
	var r json.RawMessage
	if domains == nil {
		domains = []string{}
	}
	if cidrs == nil {
		cidrs = []string{}
	}
	return r, a.call(callTimeout, methodBypassSet, map[string][]string{"domains": domains, "cidrs": cidrs}, &r)
}

// ForwardRule is one port forward as the frontend edits it.
type ForwardRule struct {
	Proto  string `json:"proto"`
	Port   uint16 `json:"port"`
	Target string `json:"target"`
	Name   string `json:"name"`
}

// Forwards returns the port forwards with their state ({server, forwards}).
func (a *App) Forwards() (json.RawMessage, error) {
	var r json.RawMessage
	return r, a.call(callTimeout, methodFwdList, nil, &r)
}

// SetForwards replaces the port forwards.
func (a *App) SetForwards(l []ForwardRule) (json.RawMessage, error) {
	var r json.RawMessage
	if l == nil {
		l = []ForwardRule{}
	}
	return r, a.call(callTimeout, methodFwdSet, map[string][]ForwardRule{"forwards": l}, &r)
}

// Usage returns the usage history from day from ("YYYY-MM-DD", "" = all).
func (a *App) Usage(from string) (json.RawMessage, error) {
	var r json.RawMessage
	return r, a.call(callTimeout, methodUsageGet, map[string]string{"from": from}, &r)
}

// NotifyPrefs returns the notification toggles per event type.
func (a *App) NotifyPrefs() NotifyPrefs { return a.notify.Prefs() }

// SetNotifyPrefs saves the notification toggles.
func (a *App) SetNotifyPrefs(p NotifyPrefs) error { return a.notify.SetPrefs(p) }

// onEvent shows a hub event as an OS notification.
func (a *App) onEvent(params json.RawMessage) {
	var e hubEvent
	if err := json.Unmarshal(params, &e); err != nil {
		a.log.Debug("hub event", "err", err)
		return
	}
	a.notify.handle(e, time.Now())
}
