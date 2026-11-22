package admin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// TOTPIssuer is the issuer shown in authenticator apps.
const TOTPIssuer = "CrossStream"

// Options are the dependencies of a Service.
type Options struct {
	Backend Backend
	Users   UserStore
	Audit   *Audit          // nil = memory only
	Logs    func() []string // recent log lines, already redacted (nil = none)
	Log     *slog.Logger
	Now     func() time.Time
}

// Service is every admin operation: the hub operations of the Backend plus
// admin users, the audit log and recent log lines. Every write is recorded
// in the audit log with the actor from ctx (WithActor).
type Service struct {
	o Options

	mu       sync.Mutex
	totpLast map[string]uint64 // newest TOTP step used per user (codes work once)
	dummy    string            // hash checked for unknown users (same timing)

	// hashSem bounds concurrent argon2id checks: each takes tens of MiB, and
	// the login rate limit is per IP only.
	hashSem chan struct{}
}

// maxConcurrentHashes is how many logins are verified at once; the rest wait.
const maxConcurrentHashes = 2

// NewService returns a Service over o.
func NewService(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Audit == nil {
		o.Audit, _ = OpenAudit("", o.Now)
	}
	return &Service{o: o, totpLast: map[string]uint64{}, hashSem: make(chan struct{}, maxConcurrentHashes)}
}

func (s *Service) record(ctx context.Context, action, target, detail string, err error) {
	if aerr := s.o.Audit.Record(ctx, action, target, detail, err); aerr != nil {
		s.o.Log.Warn("audit log write failed", "err", aerr)
	}
}

// Record adds an audit entry for an action outside the Service (web login, logout).
func (s *Service) Record(ctx context.Context, action, target string, err error) {
	s.record(ctx, action, target, "", err)
}

// Enroll creates an enrollment code (audited; the code itself is not logged).
func (s *Service) Enroll(ctx context.Context, name string) (EnrollResult, error) {
	r, err := s.o.Backend.Enroll(ctx, name)
	s.record(ctx, "enroll", name, "", err)
	return r, err
}

// Hubs lists enrolled hubs.
func (s *Service) Hubs(ctx context.Context) ([]Hub, error) { return s.o.Backend.Hubs(ctx) }

// Sessions lists active sessions.
func (s *Service) Sessions(ctx context.Context) ([]Session, error) { return s.o.Backend.Sessions(ctx) }

// Info describes the server.
func (s *Service) Info(ctx context.Context) (ServerInfo, error) { return s.o.Backend.Info(ctx) }

// Revoke deletes a hub (audited).
func (s *Service) Revoke(ctx context.Context, name string) error {
	err := s.o.Backend.Revoke(ctx, name)
	s.record(ctx, "revoke", name, "", err)
	return err
}

// Rename renames a hub (audited).
func (s *Service) Rename(ctx context.Context, name, newName string) error {
	err := s.o.Backend.Rename(ctx, name, newName)
	s.record(ctx, "rename", name, "to "+newName, err)
	return err
}

// ForwardAllow sets a hub's port forward allowance (audited).
func (s *Service) ForwardAllow(ctx context.Context, name string, n int) error {
	err := s.o.Backend.ForwardAllow(ctx, name, n)
	s.record(ctx, "forward_allow", name, fmt.Sprintf("%d ports", n), err)
	return err
}

// SetTunnelPort changes the configured tunnel port (audited).
func (s *Service) SetTunnelPort(ctx context.Context, port int) error {
	err := s.o.Backend.SetTunnelPort(ctx, port)
	s.record(ctx, "tunnel_port", "", fmt.Sprintf("to %d", port), err)
	return err
}

// Restart restarts the server (audited before it goes down).
func (s *Service) Restart(ctx context.Context) error {
	s.record(ctx, "restart", "", "", nil)
	return s.o.Backend.Restart(ctx)
}

// AuditLog returns up to n newest audit entries, newest first.
func (s *Service) AuditLog(_ context.Context, n int) []AuditEntry { return s.o.Audit.Last(n) }

// Logs returns up to n newest redacted log lines, oldest first.
func (s *Service) Logs(_ context.Context, n int) []string {
	if s.o.Logs == nil {
		return []string{}
	}
	l := s.o.Logs()
	if n > 0 && len(l) > n {
		l = l[len(l)-n:]
	}
	return l
}

// AddUser creates an admin user (audited, the password is never logged).
func (s *Service) AddUser(ctx context.Context, name, password string) error {
	err := s.putPassword(name, password, true)
	s.record(ctx, "admin_add", name, "", err)
	return err
}

// SetPassword changes a user's password; their web sessions end (audited).
func (s *Service) SetPassword(ctx context.Context, name, password string) error {
	err := s.putPassword(name, password, false)
	s.record(ctx, "admin_passwd", name, "", err)
	return err
}

func (s *Service) putPassword(name, password string, create bool) error {
	if err := ValidUserName(name); err != nil {
		return err
	}
	if err := ValidPassword(password); err != nil {
		return err
	}
	now := s.o.Now().UTC()
	u, ok := s.o.Users.User(name)
	switch {
	case create && ok:
		return fmt.Errorf("%q: %w", name, ErrUserExists)
	case !create && !ok:
		return fmt.Errorf("%q: %w", name, ErrUserNotFound)
	case create:
		u = UserRecord{Name: name, Created: now}
	}
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	u.Hash, u.Changed = h, now
	return s.o.Users.PutUser(u, create)
}

// DelUser deletes an admin user; their web sessions end (audited).
func (s *Service) DelUser(ctx context.Context, name string) error {
	err := s.o.Users.DelUser(name)
	s.record(ctx, "admin_del", name, "", err)
	return err
}

// Users lists admin users without secrets.
func (s *Service) Users(context.Context) []UserInfo {
	recs := s.o.Users.Users()
	out := make([]UserInfo, 0, len(recs))
	for _, u := range recs {
		out = append(out, u.Info())
	}
	return out
}

// SetTOTP turns a user's TOTP on with a fresh secret (returns the otpauth
// URI) or off (audited; the secret is never logged).
func (s *Service) SetTOTP(ctx context.Context, name string, on bool) (string, error) {
	uri, err := s.setTOTP(name, on)
	detail := "off"
	if on {
		detail = "on"
	}
	s.record(ctx, "admin_totp", name, detail, err)
	return uri, err
}

func (s *Service) setTOTP(name string, on bool) (string, error) {
	u, ok := s.o.Users.User(name)
	if !ok {
		return "", fmt.Errorf("%q: %w", name, ErrUserNotFound)
	}
	u.TOTP, u.Changed = "", s.o.Now().UTC()
	if on {
		sec, err := NewTOTPSecret()
		if err != nil {
			return "", err
		}
		u.TOTP = sec
	}
	if err := s.o.Users.PutUser(u, false); err != nil {
		return "", err
	}
	if !on {
		return "", nil
	}
	return OTPAuthURI(TOTPIssuer, name, u.TOTP), nil
}

// Authenticate checks a web login. Every failure is ErrBadLogin, except a
// right password for a TOTP user without a code: ErrTOTPRequired. Unknown
// users cost the same argon2id work as known ones.
func (s *Service) Authenticate(ctx context.Context, name, password, code string) error {
	select {
	case s.hashSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	u, ok := s.o.Users.User(name)
	hash := u.Hash
	if !ok {
		hash = s.dummyHash()
	}
	match := VerifyPassword(hash, password)
	<-s.hashSem
	if !ok || !match {
		return ErrBadLogin
	}
	if u.TOTP == "" {
		return nil
	}
	if code == "" {
		return ErrTOTPRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := VerifyTOTP(u.TOTP, code, s.o.Now(), s.totpLast[name])
	if !ok {
		return ErrBadLogin
	}
	s.totpLast[name] = n
	return nil
}

// UserChanged returns when a user's credentials last changed; ok is false
// for a deleted user. Web sessions older than the change are ended.
func (s *Service) UserChanged(name string) (time.Time, bool) {
	u, ok := s.o.Users.User(name)
	return u.Changed, ok
}

func (s *Service) dummyHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dummy == "" {
		s.dummy, _ = HashPassword("not a password")
	}
	return s.dummy
}
