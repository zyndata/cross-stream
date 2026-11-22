package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeService struct{ revoked []string }

func (f *fakeService) Rename(_ context.Context, name, newName string) error {
	if name != "t1" || newName == "" {
		return errors.New("hub not found")
	}
	return nil
}

func (f *fakeService) SetTunnelPort(_ context.Context, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("tunnel port out of range")
	}
	return nil
}

func (f *fakeService) Restart(context.Context) error {
	return errors.New("restart is not available here")
}

func (f *fakeService) Info(context.Context) (ServerInfo, error) {
	return ServerInfo{Version: "v9", UDPPorts: []int{51900}}, nil
}

func (f *fakeService) Enroll(_ context.Context, name string) (EnrollResult, error) {
	if name == "" {
		return EnrollResult{}, errors.New("empty name")
	}
	return EnrollResult{Code: "XS1.abc", Expires: time.Unix(10, 0).UTC()}, nil
}

func (f *fakeService) Hubs(context.Context) ([]Hub, error) {
	return []Hub{{Name: "t1", TunnelIP: "100.64.0.2", Online: true}}, nil
}

func (f *fakeService) Revoke(_ context.Context, name string) error {
	if name != "t1" {
		return errors.New("hub not found")
	}
	f.revoked = append(f.revoked, name)
	return nil
}

func (f *fakeService) ForwardAllow(_ context.Context, name string, n int) error {
	if name != "t1" || n > 20 {
		return errors.New("bad allowance")
	}
	return nil
}

func (f *fakeService) Sessions(context.Context) ([]Session, error) {
	return []Session{{Hub: "t1", Paths: []Path{{ID: 0, Addr: "1.2.3.4:5"}}, RxBytes: 9}}, nil
}

func TestSocketRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc := &fakeService{}
	done := make(chan error, 1)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	adm := NewService(Options{Backend: svc, Users: newMemUsers(), Log: quiet})
	go func() { done <- Serve(ctx, ln, adm, quiet) }()
	c := Client{Socket: sock}

	tests := []struct {
		name    string
		call    func() (string, error)
		want    string
		wantErr string
	}{
		{"enroll", func() (string, error) { r, err := c.Enroll(ctx, "t1"); return r.Code, err }, "XS1.abc", ""},
		{"enroll error", func() (string, error) { r, err := c.Enroll(ctx, ""); return r.Code, err }, "", "empty name"},
		{"hubs", func() (string, error) { h, err := c.Hubs(ctx); return h[0].TunnelIP, err }, "100.64.0.2", ""},
		{"revoke", func() (string, error) { return "", c.Revoke(ctx, "t1") }, "", ""},
		{"revoke missing", func() (string, error) { return "", c.Revoke(ctx, "nope") }, "", "hub not found"},
		{"forward allow", func() (string, error) { return "", c.ForwardAllow(ctx, "t1", 3) }, "", ""},
		{"forward allow bad", func() (string, error) { return "", c.ForwardAllow(ctx, "t1", 21) }, "", "bad allowance"},
		{"sessions", func() (string, error) { s, err := c.Sessions(ctx); return s[0].Paths[0].Addr, err }, "1.2.3.4:5", ""},
		{"rename", func() (string, error) { return "", c.Rename(ctx, "t1", "t2") }, "", ""},
		{"rename missing", func() (string, error) { return "", c.Rename(ctx, "x", "t2") }, "", "hub not found"},
		{"info", func() (string, error) { i, err := c.Info(ctx); return i.Version, err }, "v9", ""},
		{"tunnel port", func() (string, error) { return "", c.SetTunnelPort(ctx, 51901) }, "", ""},
		{"tunnel port bad", func() (string, error) { return "", c.SetTunnelPort(ctx, 0) }, "", "out of range"},
		{"restart unavailable", func() (string, error) { return "", c.Restart(ctx) }, "", "not available"},
		{"admin add", func() (string, error) { return "", c.AddUser(ctx, "alice", "correct horse") }, "", ""},
		{"admin add short", func() (string, error) { return "", c.AddUser(ctx, "bob", "short") }, "", "at least 8"},
		{"admin add dup", func() (string, error) { return "", c.AddUser(ctx, "alice", "correct horse") }, "", "already exists"},
		{"admin passwd", func() (string, error) { return "", c.SetPassword(ctx, "alice", "battery staple") }, "", ""},
		{"admin passwd missing", func() (string, error) { return "", c.SetPassword(ctx, "bob", "battery staple") }, "", "not found"},
		{"admin totp", func() (string, error) {
			u, err := c.SetTOTP(ctx, "alice", true)
			return u[:len("otpauth://totp/CrossStream:alice?")], err
		}, "otpauth://totp/CrossStream:alice?", ""},
		{"admin list", func() (string, error) { u, err := c.Users(ctx); return fmt.Sprint(u[0].Name, u[0].TOTP), err }, "alicetrue", ""},
		{"admin del", func() (string, error) { return "", c.DelUser(ctx, "alice") }, "", ""},
		{"admin del missing", func() (string, error) { return "", c.DelUser(ctx, "alice") }, "", "not found"},
		{"audit", func() (string, error) {
			a, err := c.AuditLog(ctx, 1)
			return fmt.Sprint(len(a), a[0].Action, a[0].Actor, a[0].OK), err
		}, "1admin_delxs-ctlfalse", ""},
		{"unknown op", func() (string, error) { _, err := c.call(ctx, Request{Op: "x"}); return "", err }, "", "unknown op"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if len(svc.revoked) != 1 {
		t.Fatalf("revoked %v", svc.revoked)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if _, err := (Client{Socket: sock}).Hubs(context.Background()); err == nil {
		t.Fatal("call after shutdown succeeded")
	}
}

// memUsers is an in-memory UserStore.
type memUsers struct {
	mu sync.Mutex
	m  map[string]UserRecord
}

func newMemUsers() *memUsers { return &memUsers{m: map[string]UserRecord{}} }

func (u *memUsers) Users() []UserRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]UserRecord, 0, len(u.m))
	for _, r := range u.m {
		out = append(out, r)
	}
	return out
}

func (u *memUsers) User(name string) (UserRecord, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.m[name]
	return r, ok
}

func (u *memUsers) PutUser(r UserRecord, create bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.m[r.Name]
	if create && ok {
		return ErrUserExists
	}
	if !create && !ok {
		return ErrUserNotFound
	}
	u.m[r.Name] = r
	return nil
}

func (u *memUsers) DelUser(name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.m[name]; !ok {
		return ErrUserNotFound
	}
	delete(u.m, name)
	return nil
}
