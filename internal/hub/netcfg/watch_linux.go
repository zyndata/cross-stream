package netcfg

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// netlinkEvents signals (coalesced) on every rtnetlink message of the IPv4
// route, rule, address and link groups. The channel closes when ctx ends.
func netlinkEvents(ctx context.Context) (<-chan struct{}, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK,
		Groups: unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV4_RULE}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("netlink bind: %w", err)
	}
	f := os.NewFile(uintptr(fd), "netlink") // non-blocking fd: Close unblocks Read
	ch := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()
		_ = f.Close()
	}()
	go func() {
		defer close(ch)
		buf := make([]byte, 64<<10)
		for {
			if _, err := f.Read(buf); err != nil {
				if ctx.Err() != nil {
					return
				}
				continue // ENOBUFS after a burst: keep listening
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch, nil
}
