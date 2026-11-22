package server

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/zyndata/cross-stream/internal/server/admin"
	"github.com/zyndata/cross-stream/internal/wire"
)

// AuditFile is the audit log in the data directory.
const AuditFile = "audit.jsonl"

// newAdmin builds the admin service over s (control socket and web panel).
func (s *Server) newAdmin() *admin.Service {
	var audit *admin.Audit
	if s.o.Config.DataDir != "" {
		a, err := admin.OpenAudit(filepath.Join(s.o.Config.DataDir, AuditFile), s.o.Now)
		if err != nil {
			s.log.Warn("audit log not loaded, starting empty", "err", err)
		}
		audit = a
	}
	return admin.NewService(admin.Options{Backend: s, Users: s.o.State, Audit: audit, Logs: s.o.Logs, Log: s.log, Now: s.o.Now})
}

// Admin returns the admin service (the web panel serves it).
func (s *Server) Admin() *admin.Service { return s.adm }

// Rename implements admin.Backend. A hub online under the old name is told
// to reconnect (CLOSE normal): its next session carries the new name, so no
// running session ever changes its name under the packet path.
func (s *Server) Rename(_ context.Context, name, newName string) error {
	if err := s.o.State.Rename(name, newName); err != nil {
		return err
	}
	s.mu.Lock()
	hs := s.byName[name]
	if ts, ok := s.initTS[name]; ok {
		// The hub may have reconnected under the new name already: keep the newest.
		s.initTS[newName] = max(ts, s.initTS[newName])
		delete(s.initTS, name)
	}
	s.mu.Unlock()
	if hs != nil {
		_ = hs.sess.Send(&wire.Close{Reason: wire.CloseNormal})
		s.removeSession(hs)
	}
	s.log.Info("hub renamed", "hub", name, "new_name", newName, "reconnect", hs != nil)
	return nil
}

// Info implements admin.Backend.
func (s *Server) Info(ctx context.Context) (admin.ServerInfo, error) {
	c := s.o.Config
	in := admin.ServerInfo{Version: s.o.Version, Started: s.started, WSSPath: c.WSSPath,
		MinHubVersion: c.MinHubVersion, PEP: c.PEP, TunnelPrefix: TunnelPrefix.String(),
		TokenTTL: c.TokenTTL.String(), EgressAllow: slices.Clone(c.EgressAllow), UDPPorts: []int{}}
	if in.EgressAllow == nil {
		in.EgressAllow = []string{}
	}
	for _, a := range s.Addrs() {
		in.UDPPorts = append(in.UDPPorts, int(a.Port()))
	}
	in.TCPPorts, in.WSSPorts = s.StreamPorts()
	ports, _ := s.codePorts()
	s.tunnelInfo(ctx, &in)
	eps, err := Endpoints(c.PublicEndpoints, ports, s.o.Detect)
	if err != nil {
		in.EndpointsErr = err.Error()
	}
	in.Endpoints = eps
	if in.Endpoints == nil {
		in.Endpoints = []string{}
	}
	return in, nil
}
