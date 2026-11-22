package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cheap argon2id parameters keep the tests fast; VerifyPassword reads them
// from the hash.
var testArgon = argonParams{mem: 1024, time: 1, threads: 1}

func TestMain(m *testing.M) {
	defaultArgon = testArgon
	os.Exit(m.Run())
}

func TestPasswordHash(t *testing.T) {
	h, err := hashWith("correct horse", testArgon)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := hashWith("correct horse", testArgon)
	if h == h2 {
		t.Fatal("same hash twice: salt not random")
	}
	parts := strings.Split(h, "$")
	tests := []struct {
		name, hash, pw string
		want           bool
	}{
		{"right", h, "correct horse", true},
		{"wrong", h, "correct horsE", false},
		{"empty", h, "", false},
		{"not phc", "plain", "correct horse", false},
		{"argon2i", strings.Replace(h, "argon2id", "argon2i", 1), "correct horse", false},
		{"bad version", strings.Replace(h, "v=19", "v=16", 1), "correct horse", false},
		{"memory bomb", "$argon2id$v=19$m=4194304,t=1,p=1$" + parts[4] + "$" + parts[5], "correct horse", false},
		{"bad salt", "$argon2id$v=19$m=1024,t=1,p=1$!!$" + parts[5], "correct horse", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPassword(tt.hash, tt.pw); got != tt.want {
				t.Fatalf("VerifyPassword = %v, want %v", got, tt.want)
			}
		})
	}
	if full, err := hashWith("x", productionArgon); err != nil || !strings.HasPrefix(full, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("HashPassword = %q, %v", full, err)
	}
}

func TestPolicy(t *testing.T) {
	tests := []struct {
		name, user, pw string
		ok             bool
	}{
		{"ok", "alice", "12345678", true},
		{"dots", "a.b-c_d", "12345678", true},
		{"short pw", "alice", "1234567", false},
		{"empty user", "", "12345678", false},
		{"space", "al ice", "12345678", false},
		{"long user", strings.Repeat("a", 33), "12345678", false},
		{"unicode pw counts runes", "alice", "zażółćgę", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.Join(ValidUserName(tt.user), ValidPassword(tt.pw))
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

// RFC 6238 appendix B (SHA-1 seed "12345678901234567890"), last 6 digits.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPCode(t *testing.T) {
	tests := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, tt := range tests {
		got, err := TOTPCode(rfcSecret, TOTPCounter(time.Unix(tt.unix, 0)))
		if err != nil || got != tt.want {
			t.Fatalf("T=%d: %q, %v; want %q", tt.unix, got, err, tt.want)
		}
	}
	if _, err := TOTPCode("!!!", 1); err == nil {
		t.Fatal("bad secret accepted")
	}
}

func TestVerifyTOTPWindow(t *testing.T) {
	now := time.Unix(1234567890, 0)
	c := TOTPCounter(now)
	code := func(n uint64) string { s, _ := TOTPCode(rfcSecret, n); return s }
	tests := []struct {
		name string
		code string
		last uint64
		ok   bool
	}{
		{"now", code(c), 0, true},
		{"previous step", code(c - 1), 0, true},
		{"next step", code(c + 1), 0, true},
		{"two steps old", code(c - 2), 0, false},
		{"two steps ahead", code(c + 2), 0, false},
		{"reused", code(c), c, false},
		{"older than last used", code(c - 1), c - 1, false},
		{"newer than last used", code(c + 1), c, true},
		{"spaces trimmed", " " + code(c) + " ", 0, true},
		{"wrong length", code(c)[:5], 0, false},
		{"wrong", "000000", 0, code(c) == "000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := VerifyTOTP(rfcSecret, tt.code, now, tt.last); ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
		})
	}
	sec, err := NewTOTPSecret()
	if err != nil || len(sec) != 32 {
		t.Fatalf("NewTOTPSecret = %q, %v", sec, err)
	}
	uri := OTPAuthURI("CrossStream", "al ice", sec)
	if !strings.HasPrefix(uri, "otpauth://totp/CrossStream:al%20ice?") || !strings.Contains(uri, "secret="+sec) {
		t.Fatalf("uri %q", uri)
	}
}

func TestAuthenticate(t *testing.T) {
	now := time.Unix(1234567890, 0)
	users := newMemUsers()
	svc := NewService(Options{Users: users, Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx := WithActor(context.Background(), "test")
	if err := svc.AddUser(ctx, "alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddUser(ctx, "bob", "battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetTOTP(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	bob, _ := users.User("bob")
	code, _ := TOTPCode(bob.TOTP, TOTPCounter(now))
	tests := []struct {
		name, user, pw, code string
		want                 error
	}{
		{"password only", "alice", "correct horse", "", nil},
		{"wrong password", "alice", "wrong horse", "", ErrBadLogin},
		{"unknown user", "carol", "correct horse", "", ErrBadLogin},
		{"totp missing", "bob", "battery staple", "", ErrTOTPRequired},
		{"totp wrong password", "bob", "wrong", code, ErrBadLogin},
		{"totp ok", "bob", "battery staple", code, nil},
		{"totp replay", "bob", "battery staple", code, ErrBadLogin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := svc.Authenticate(ctx, tt.user, tt.pw, tt.code); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	// Password change moves Changed (web sessions end).
	before, _ := svc.UserChanged("alice")
	now = now.Add(time.Minute)
	if err := svc.SetPassword(ctx, "alice", "new password"); err != nil {
		t.Fatal(err)
	}
	if after, ok := svc.UserChanged("alice"); !ok || !after.After(before) {
		t.Fatalf("Changed %v -> %v", before, after)
	}
	// Nothing secret in the audit log.
	for _, e := range svc.AuditLog(ctx, 0) {
		s := e.Action + e.Target + e.Detail + e.Error
		if strings.Contains(s, "horse") || strings.Contains(s, "staple") || strings.Contains(s, bob.TOTP) {
			t.Fatalf("secret in audit entry %+v", e)
		}
	}
}

func TestAuditFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := OpenAudit(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(context.Background(), "web:alice@127.0.0.1")
	_ = a.Record(ctx, "enroll", "h1", "", nil)
	_ = a.Record(ctx, "revoke", "h2", "", errors.New("hub not found"))
	b, err := OpenAudit(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := b.Last(0)
	if len(got) != 2 || got[0].Action != "revoke" || got[0].OK || got[0].Error != "hub not found" ||
		got[1].Actor != "web:alice@127.0.0.1" || !got[1].OK {
		t.Fatalf("reloaded %+v", got)
	}
	if l := b.Last(1); len(l) != 1 || l[0].Action != "revoke" {
		t.Fatalf("Last(1) = %+v", l)
	}
	if Actor(context.Background()) != "unknown" {
		t.Fatal("default actor")
	}
	m, _ := OpenAudit("", nil)
	for i := 0; i < AuditKeep+5; i++ {
		_ = m.Record(ctx, "x", "", "", nil)
	}
	if n := len(m.Last(0)); n != AuditKeep {
		t.Fatalf("kept %d", n)
	}
}
