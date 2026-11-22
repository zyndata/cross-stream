package tun

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// configure sets the IPv4 address and netmask of the interface and brings it up.
func configure(name string, cfg Config) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	addr := cfg.Prefix.Addr().As4()
	if err := ifreqAddr(fd, name, unix.SIOCSIFADDR, addr[:]); err != nil {
		return fmt.Errorf("set address: %w", err)
	}
	mask := net.CIDRMask(cfg.Prefix.Bits(), 32)
	if err := ifreqAddr(fd, name, unix.SIOCSIFNETMASK, mask); err != nil {
		return fmt.Errorf("set netmask: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("ifreq: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("get flags: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("set flags: %w", err)
	}
	return nil
}

func ifreqAddr(fd int, name string, req uint, v []byte) error {
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("ifreq: %w", err)
	}
	if err := ifr.SetInet4Addr(v); err != nil {
		return fmt.Errorf("ifreq addr: %w", err)
	}
	if err := unix.IoctlIfreq(fd, req, ifr); err != nil {
		return fmt.Errorf("ioctl: %w", err)
	}
	return nil
}

// SetMTU changes the MTU of an existing interface.
func SetMTU(name string, mtu int) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("ifreq: %w", err)
	}
	ifr.SetUint32(uint32(mtu))
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFMTU, ifr); err != nil {
		return fmt.Errorf("set mtu %d on %s: %w", mtu, name, err)
	}
	return nil
}
