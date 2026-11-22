//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

const defaultDir = "/var/lib/crossstream"

// hotspotConfig sets up the NetworkManager hotspot on wifi (default: the
// first Wi-Fi device) and the optional Ethernet share port.
func hotspotConfig(ctx context.Context, cfg *hub.DaemonConfig, wifi, eth, natMode string) error {
	if natMode != "auto" && natMode != "off" {
		return errors.New("-hotspot-nat is for Windows hubs (Linux NATs with nft)")
	}
	if os.Geteuid() != 0 {
		return errors.New("the hotspot needs root: run xs-hubd as the systemd service (deploy/hub/install.sh)")
	}
	if wifi == "" {
		var err error
		if wifi, err = netcfg.WifiDevice(ctx, nil); err != nil {
			return err
		}
	}
	cfg.LanIface, cfg.EthIface, cfg.DNS = wifi, eth, true
	cfg.Hotspot = &netcfg.Hotspot{}
	cfg.Inhibitor = &netcfg.Inhibitor{Why: "CrossStream hub is sharing its connection"}
	return nil
}

// runService runs xs-hubd under a service manager; systemd needs nothing
// special, so it reports false.
func runService(func(context.Context, []string, io.Writer) error) (bool, error) {
	return false, nil
}

// raisePriority does nothing on Linux (the scheduler copes; -normal-priority
// is for Windows).
func raisePriority() error { return nil }
