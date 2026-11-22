package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Client Wi-Fi choice (phase 18): xs-hubd found its hotspot card connected
// to a Wi-Fi network and waits until the owner picks share or disconnect.
const (
	eventClientWifi = "events.client_wifi"
	// choiceRestart: after a choice the view says "starting" this long
	// while xs-hubd brings the hotspot up (NetworkManager may take ~30 s).
	choiceRestart = 90 * time.Second
)

// clientWifiQ is xs-hubd's question (hub.ClientWifiQuestion).
type clientWifiQ struct {
	Iface    string `json:"iface"`
	Conn     string `json:"conn"`
	CanShare bool   `json:"can_share"`
	Tried    string `json:"tried,omitempty"`
	Error    string `json:"error,omitempty"`
}

// askClientWifi shows the question; the stream stays open until a choice.
func (a *App) askClientWifi(ctx context.Context, params json.RawMessage) error {
	var q clientWifiQ
	if err := json.Unmarshal(params, &q); err != nil {
		return fmt.Errorf("client wifi: %w", err)
	}
	a.mu.Lock()
	a.wasUp, a.startErr, a.chosenAt = false, "", time.Time{}
	a.mu.Unlock()
	a.publish(ctx, View{State: stateWifi, ClientWifi: &q, Relays: []RelayView{}, Uplinks: []UplinkView{}})
	return nil
}

// ClientWifiChoice answers xs-hubd: "share" (hotspot next to the network)
// or "disconnect". xs-hubd saves it and starts again.
func (a *App) ClientWifiChoice(choice string) error {
	if choice != "share" && choice != "disconnect" {
		return fmt.Errorf("client wifi choice %q", choice)
	}
	if err := a.call(callTimeout, methodHotspotSet, map[string]string{"client_wifi": choice}, nil); err != nil {
		return err
	}
	a.mu.Lock()
	a.chosenAt = time.Now()
	ctx, v := a.ctx, a.last
	a.mu.Unlock()
	if ctx != nil {
		v.State, v.Starting, v.ClientWifi = stateNoDaemon, true, nil
		a.publish(ctx, v)
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}
