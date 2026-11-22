package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

type fakeUser struct {
	pw, code string
	changed  time.Time
}

type record struct {
	actor, action, target string
	ok                    bool
}

// fakeAdmin implements Admin in memory.
type fakeAdmin struct {
	mu       sync.Mutex
	users    map[string]*fakeUser
	records  []record
	hubs     []admin.Hub
	sessions []admin.Session
}

func newFakeAdmin() *fakeAdmin {
	return &fakeAdmin{
		users: map[string]*fakeUser{"alice": {pw: "correct horse"}, "bob": {pw: "battery staple", code: "123456"}},
		hubs:  []admin.Hub{{Name: "h1", TunnelIP: "100.64.0.2", Online: true}, {Name: "h2", TunnelIP: "100.64.0.3"}},
		sessions: []admin.Session{{Hub: "h1", RxRate: 1000, TxRate: 3000,
			Paths: []admin.Path{{ID: 1, Addr: "198.51.100.7:4000"}}}},
	}
}

func (f *fakeAdmin) Authenticate(_ context.Context, user, pw, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok || u.pw != pw {
		return admin.ErrBadLogin
	}
	if u.code != "" && code == "" {
		return admin.ErrTOTPRequired
	}
	if u.code != code {
		return admin.ErrBadLogin
	}
	return nil
}

func (f *fakeAdmin) UserChanged(user string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[user]
	if !ok {
		return time.Time{}, false
	}
	return u.changed, true
}

func (f *fakeAdmin) Users(context.Context) []admin.UserInfo {
	return []admin.UserInfo{{Name: "alice"}, {Name: "bob", TOTP: true}}
}

func (f *fakeAdmin) Record(ctx context.Context, action, target string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, record{admin.Actor(ctx), action, target, err == nil})
}

func (f *fakeAdmin) Enroll(ctx context.Context, name string) (admin.EnrollResult, error) {
	f.Record(ctx, "enroll", name, nil)
	if name == "" {
		return admin.EnrollResult{}, errors.New("hub name: 1-64 bytes")
	}
	return admin.EnrollResult{Code: "XS1.code", Expires: time.Unix(100, 0).UTC()}, nil
}

func (f *fakeAdmin) Hubs(context.Context) ([]admin.Hub, error) { return f.hubs, nil }

func (f *fakeAdmin) Revoke(ctx context.Context, name string) error {
	if name != "h1" {
		return errors.New(`revoke "` + name + `": hub not found`)
	}
	f.Record(ctx, "revoke", name, nil)
	return nil
}

func (f *fakeAdmin) Rename(ctx context.Context, name, _ string) error {
	f.Record(ctx, "rename", name, nil)
	return nil
}

func (f *fakeAdmin) Sessions(context.Context) ([]admin.Session, error) { return f.sessions, nil }

func (f *fakeAdmin) ForwardAllow(ctx context.Context, name string, n int) error {
	if n > 20 {
		return errors.New("forward allow: 21: want 0-20")
	}
	f.Record(ctx, "forward_allow", name, nil)
	return nil
}

func (f *fakeAdmin) Info(context.Context) (admin.ServerInfo, error) {
	return admin.ServerInfo{Version: "v1.2.3", UDPPorts: []int{51900}}, nil
}

func (f *fakeAdmin) SetTunnelPort(ctx context.Context, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("tunnel port out of range")
	}
	f.Record(ctx, "tunnel_port", "", nil)
	return nil
}

func (f *fakeAdmin) Restart(ctx context.Context) error {
	f.Record(ctx, "restart", "", nil)
	return nil
}

func (f *fakeAdmin) AuditLog(context.Context, int) []admin.AuditEntry {
	return []admin.AuditEntry{{Action: "enroll", Target: "h1", OK: true}}
}

func (f *fakeAdmin) Logs(context.Context, int) []string { return []string{"level=INFO msg=hello"} }

func (f *fakeAdmin) last() record {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) == 0 {
		return record{}
	}
	return f.records[len(f.records)-1]
}

// testClock is a settable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t   *testing.T
	adm *fakeAdmin
	clk *testClock
	srv *Server
	h   http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	adm := newFakeAdmin()
	srv := New(Options{Admin: adm, Now: clk.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &env{t: t, adm: adm, clk: clk, srv: srv, h: srv.Handler()}
}

const testHost = "localhost:8443"

// do sends one request from ip (default 192.0.2.1) with optional session
// cookie and CSRF token.
func (e *env) do(method, path, body, cookie, csrf string, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, "https://"+testHost+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:5555"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	if csrf != "" {
		r.Header.Set(CSRFHeader, csrf)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "RemoteAddr" {
			r.RemoteAddr = hdr[i+1]
			continue
		}
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// login logs alice in and returns the cookie value and CSRF token.
func (e *env) login() (cookie, csrf string) {
	e.t.Helper()
	w := e.do("POST", "/api/login", `{"user":"alice","password":"correct horse"}`, "", "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie {
			cookie = c.Value
		}
	}
	csrf = jsonField(e.t, w.Body.String(), "csrf")
	return cookie, csrf
}
