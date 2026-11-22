package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// ErrNoRestart means this server cannot restart itself (no restarter set).
var ErrNoRestart = errors.New("restart is not available here; restart the server yourself")

// tunnelInfo fills the tunnel port fields of the server info: the port in
// use, the one configured in the PortSource now, and whether a restart is
// needed to apply it.
func (s *Server) tunnelInfo(ctx context.Context, in *admin.ServerInfo) {
	_, running := s.codePorts()
	in.TunnelPort, in.Reenroll = running, s.o.State.Reenroll(running)
	in.CanRestart = s.o.Restart != nil
	in.TunnelPortConfigured = running
	src := s.o.Ports
	if src == nil {
		in.TunnelPortSource = "default"
		return
	}
	in.TunnelPortSource, in.TunnelPortWritable = src.Name(), src.Writable()
	p, err := src.Configured(ctx)
	if err != nil {
		in.TunnelPortErr = err.Error()
		return
	}
	if p == 0 {
		p = s.mainPort()
	}
	in.TunnelPortConfigured = p
	in.RestartRequired = p != running
}

// mainPort is the first listen port as bound (port 0 in tests resolves).
func (s *Server) mainPort() int {
	if a := s.Addrs(); len(a) > 0 {
		return int(a[0].Port())
	}
	return s.o.Config.ListenPorts[0]
}

// SetTunnelPort implements admin.Backend: it changes the port in the
// PortSource only; the server keeps using the current port until it restarts.
func (s *Server) SetTunnelPort(ctx context.Context, port int) error {
	if s.o.Ports == nil || !s.o.Ports.Writable() {
		return ErrPortReadOnly
	}
	if err := s.checkTunnelPort(port); err != nil {
		return err
	}
	if err := s.o.Ports.Set(ctx, port); err != nil {
		return err
	}
	s.log.Warn("tunnel port changed in the configuration; restart to apply, then enroll the existing hubs again",
		"tunnel_port", port, "source", s.o.Ports.Name())
	return nil
}

// checkTunnelPort refuses a port out of range or used by another listener.
func (s *Server) checkTunnelPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("tunnel port %d out of range 1..65535", port)
	}
	c := s.o.Config
	used := slices.Concat(c.ListenPorts[1:], c.WSSPorts)
	for _, p := range c.TCPPorts {
		if p != c.ListenPorts[0] {
			used = append(used, p)
		}
	}
	for _, l := range []string{c.WebListen, c.WebIngressListen, c.MetricsListen} {
		if _, p, err := net.SplitHostPort(l); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				used = append(used, n)
			}
		}
	}
	if slices.Contains(used, port) {
		return fmt.Errorf("port %d is already used by the server for something else", port)
	}
	return nil
}

// Restart implements admin.Backend.
func (s *Server) Restart(ctx context.Context) error {
	if s.o.Restart == nil {
		return ErrNoRestart
	}
	s.log.Info("restart requested")
	return s.o.Restart(ctx)
}
