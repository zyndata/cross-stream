package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// EventClientWifi is the one notification events.subscribe sends while
// xs-hubd waits for the client Wi-Fi choice (params = ClientWifiQuestion).
const EventClientWifi = "events.client_wifi"

// clientConner reports the Wi-Fi network a card is a client of
// (netcfg.Hotspot on Linux).
type clientConner interface {
	ClientConn(ctx context.Context, iface string) (string, error)
}

// ClientWifiQuestion asks what to do with the Wi-Fi client connection on
// the hotspot card (phase 18).
type ClientWifiQuestion struct {
	Iface    string `json:"iface"`
	Conn     string `json:"conn"`      // the network the card is connected to
	CanShare bool   `json:"can_share"` // the AP can run next to it
	Tried    string `json:"tried,omitempty"`
	Error    string `json:"error,omitempty"` // why Tried failed
}

func questionOf(cw *netcfg.ClientWifiError) ClientWifiQuestion {
	q := ClientWifiQuestion{Iface: cw.Iface, Conn: cw.Conn, CanShare: cw.CanShare, Tried: cw.Tried}
	if cw.Err != nil {
		q.Error = cw.Err.Error()
	}
	return q
}

// apIface is the card the hotspot is configured on: Linux keeps the real
// card (the AP may sit on a virtual interface next to it), Windows the
// adapter Mobile Hotspot uses.
func (d *Daemon) apIface() string {
	if d.wifi != "" {
		return d.wifi
	}
	return d.cfg.LanIface
}

// restartIfMoved restarts xs-hubd when a hotspot restart put the AP on
// another interface (onto or off a virtual interface): the LAN sockets,
// nft rules and DNS are bound to the old one.
func (d *Daemon) restartIfMoved(st netcfg.HotspotState) {
	if d.wifi == "" || st.Iface == "" || st.Iface == d.cfg.LanIface {
		return
	}
	d.log.Info("hotspot moved to another interface: restarting xs-hubd", "from", d.cfg.LanIface, "to", st.Iface)
	d.requestRestart(time.Second)
}

// AskClientWifi serves the IPC socket while the hotspot card is connected
// to Wi-Fi as a client and no choice is saved (or the saved one failed):
// events.subscribe sends the question, hotspot.set with client_wifi saves
// the answer, every other method fails with the question. It returns nil
// once a choice is saved, or when the card left that network (laptop moved
// or slept): the caller starts the daemon again.
func AskClientWifi(ctx context.Context, cfg DaemonConfig, cw *netcfg.ClientWifiError) error {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if cfg.Socket == "" {
		return cw
	}
	ln, err := ipc.Listen(cfg.Socket, 0o660)
	if err != nil {
		return errors.Join(cw, err)
	}
	if cfg.SocketGroup != "" {
		if err := ipc.Chgrp(cfg.Socket, cfg.SocketGroup); err != nil {
			log.Warn("IPC socket stays root-only", "err", err)
		}
	}
	defer func() { _ = ln.Close(); _ = os.Remove(cfg.Socket) }()
	q := questionOf(cw)
	choices := "disconnect"
	if q.CanShare {
		choices = "share|disconnect"
	}
	log.Warn("hotspot: waiting for the Wi-Fi choice (GUI, or xs-hubctl hotspot -client-wifi "+choices+")",
		"iface", q.Iface, "conn", q.Conn, "can_share", q.CanShare, "tried", q.Tried, "err", q.Error)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chosen := make(chan struct{})
	h := func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case MethodEventsSubscribe:
			return ipc.Stream(func(ctx context.Context, notify func(string, any) error) error {
				if err := notify(EventClientWifi, q); err != nil {
					return err
				}
				select {
				case <-ctx.Done():
				case <-chosen:
				}
				return nil
			}), nil
		case MethodHotspotSet:
			info, err := saveClientWifi(cfg.Dir, q, params)
			if err != nil {
				return nil, err
			}
			log.Info("hotspot: Wi-Fi choice saved", "client_wifi", info.Settings.ClientWifi)
			select {
			case <-chosen:
			default:
				close(chosen)
			}
			return info, nil
		}
		return nil, fmt.Errorf("xs-hubd waits for the Wi-Fi choice: %w", cw)
	}
	go ipc.Serve(sctx, ln, h, log)
	cc, _ := cfg.Hotspot.(clientConner)
	tick := time.NewTicker(cmp.Or(cfg.ClientWifiRecheck, 5*time.Second))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-chosen:
			time.Sleep(300 * time.Millisecond) // the answer to hotspot.set goes out first
			return nil
		case <-tick.C:
			if cc == nil {
				continue
			}
			conn, err := cc.ClientConn(ctx, q.Iface)
			if err != nil || conn == q.Conn {
				continue
			}
			log.Info("hotspot: the Wi-Fi card left the network it asked about: starting again", "was", q.Conn, "now", conn)
			return nil
		}
	}
}

// saveClientWifi applies hotspot.set params to hotspot.json; they must
// choose share (when possible) or disconnect.
func saveClientWifi(dir string, q ClientWifiQuestion, params json.RawMessage) (HotspotInfo, error) {
	var p HotspotParams
	if err := json.Unmarshal(params, &p); err != nil {
		return HotspotInfo{}, fmt.Errorf("hotspot.set: %w", err)
	}
	switch {
	case p.ClientWifi == nil || *p.ClientWifi == netcfg.ClientWifiAsk:
		return HotspotInfo{}, errors.New("hotspot.set: choose client_wifi share or disconnect")
	case !netcfg.ValidClientWifi(*p.ClientWifi):
		return HotspotInfo{}, fmt.Errorf("hotspot.set: client_wifi %q: want share or disconnect", *p.ClientWifi)
	case *p.ClientWifi == netcfg.ClientWifiShare && !q.CanShare:
		return HotspotInfo{}, fmt.Errorf("hotspot.set: %s cannot run an access point next to a Wi-Fi client: choose disconnect", q.Iface)
	}
	host, _ := os.Hostname()
	s, err := LoadHotspotSettings(dir, host)
	if err != nil {
		return HotspotInfo{}, err
	}
	s = p.apply(s)
	if err := netcfg.ValidateHotspot(hotspotConfig(s, q.Iface, "")); err != nil {
		return HotspotInfo{}, err
	}
	if err := SaveHotspotSettings(dir, s); err != nil {
		return HotspotInfo{}, err
	}
	return HotspotInfo{Enabled: true, Settings: s}, nil
}
