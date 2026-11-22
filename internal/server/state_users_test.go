package server

import (
	"errors"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

func TestStateRename(t *testing.T) {
	s, _, path := openState(t)
	if err := s.AddToken("a", newToken(t), time.Hour); err != nil {
		t.Fatal(err)
	}
	tok := newToken(t)
	if err := s.AddToken("b", tok, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enroll(tok, newKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToken("-h", newToken(t), time.Hour); err == nil {
		t.Error(`AddToken("-h") accepted a name starting with '-'`)
	}
	tok2 := newToken(t)
	_ = s.AddToken("c", tok2, time.Hour)
	if _, err := s.Enroll(tok2, newKey(t)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, from, to string
		wantErr        error // nil = ok; errAny = any error
	}{
		{"missing", "x", "y", ErrHubNotFound},
		{"taken", "b", "c", ErrHubExists},
		{"same", "b", "b", ErrHubExists},
		{"pending token name", "b", "a", errAny},
		{"empty", "b", "", errAny},
		{"control char", "b", "a\nb", errAny},
		{"ok", "b", "b2", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Rename(tt.from, tt.to)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v", err)
			case tt.wantErr == errAny && err == nil, tt.wantErr != nil && tt.wantErr != errAny && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
	re, err := OpenState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	hubs := re.Hubs()
	if len(hubs) != 2 || hubs[0].Name != "b2" || hubs[0].TunnelIP != "100.64.0.2" {
		t.Fatalf("after reload %+v", hubs)
	}
}

var errAny = errors.New("any error")

func TestStateUsers(t *testing.T) {
	s, _, path := openState(t)
	u := admin.UserRecord{Name: "alice", Hash: "$argon2id$x", Created: time.Unix(1, 0).UTC(), Changed: time.Unix(1, 0).UTC()}
	steps := []struct {
		name string
		do   func() error
		want error
	}{
		{"add", func() error { return s.PutUser(u, true) }, nil},
		{"add again", func() error { return s.PutUser(u, true) }, admin.ErrUserExists},
		{"replace missing", func() error { return s.PutUser(admin.UserRecord{Name: "bob"}, false) }, admin.ErrUserNotFound},
		{"replace", func() error { v := u; v.TOTP = "SECRET"; return s.PutUser(v, false) }, nil},
		{"add bob", func() error { return s.PutUser(admin.UserRecord{Name: "bob"}, true) }, nil},
		{"del bob", func() error { return s.DelUser("bob") }, nil},
		{"del bob again", func() error { return s.DelUser("bob") }, admin.ErrUserNotFound},
	}
	for _, st := range steps {
		if err := st.do(); !errors.Is(err, st.want) {
			t.Fatalf("%s: err = %v, want %v", st.name, err, st.want)
		}
	}
	re, err := OpenState(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := re.User("alice")
	if !ok || got.TOTP != "SECRET" || got.Hash != u.Hash || len(re.Users()) != 1 {
		t.Fatalf("after reload %+v %v (%d users)", got, ok, len(re.Users()))
	}
}
