//go:build !linux && !windows

package netcfg

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// DialVia is Linux only for now (Windows hub: phase 16).
func DialVia(string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("netcfg: DialVia is Linux only")
	}
}

// BindDevice is Linux only for now (Windows hub: phase 16).
func BindDevice(string, bool) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return errors.New("netcfg: BindDevice is Linux only") }
}
