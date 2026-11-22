//go:build !windows

package tun

import (
	"fmt"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// openDevice creates the TUN interface and configures its address.
func openDevice(cfg Config) (wgtun.Device, string, error) {
	dev, err := wgtun.CreateTUN(cfg.Name, cfg.MTU)
	if err != nil {
		return nil, "", fmt.Errorf("create tun %s: %w", cfg.Name, err)
	}
	name, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("tun name: %w", err)
	}
	if err := configure(name, cfg); err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("configure tun %s: %w", name, err)
	}
	return dev, name, nil
}
