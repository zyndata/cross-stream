package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newKey(t *testing.T) [crypto.KeyLen]byte {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return kp.Public
}

func newToken(t *testing.T) enroll.Token {
	t.Helper()
	tok, err := enroll.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func openState(t *testing.T) (*State, *clock, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	clk := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	s, err := OpenState(path, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	return s, clk, path
}

func TestStateEnrollPersistRevoke(t *testing.T) {
	s, clk, path := openState(t)
	tok1, tok2 := newToken(t), newToken(t)
	k1, k2 := newKey(t), newKey(t)
	if err := s.AddToken("a", tok1, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToken("b", tok2, time.Hour); err != nil {
		t.Fatal(err)
	}
	ha, err := s.Enroll(tok1, k1)
	if err != nil || ha.TunnelIP != "100.64.0.2" || ha.Name != "a" {
		t.Fatalf("enroll a: %+v %v", ha, err)
	}
	if _, err := s.Enroll(tok1, k2); !errors.Is(err, enroll.ErrTokenInvalid) {
		t.Fatalf("token reuse: %v", err)
	}
	hb, err := s.Enroll(tok2, k2)
	if err != nil || hb.TunnelIP != "100.64.0.3" {
		t.Fatalf("enroll b: %+v %v", hb, err)
	}

	// The file holds hashes only, never the raw token.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok1.String()) {
		t.Fatal("raw token stored in state.json")
	}

	// Reload from disk.
	s2, err := OpenState(path, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := s2.HubByKey(k2); !ok || h.Name != "b" {
		t.Fatalf("reloaded hub b: %+v %v", h, ok)
	}
	if len(s2.Hubs()) != 2 || s2.Hubs()[0].Name != "a" {
		t.Fatalf("hubs %+v", s2.Hubs())
	}

	// Revoke frees the address, which is reused.
	if _, err := s2.Revoke("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.HubByKey(k1); ok {
		t.Fatal("revoked key still known")
	}
	if _, err := s2.Revoke("a"); !errors.Is(err, ErrHubNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	tok3 := newToken(t)
	if err := s2.AddToken("c", tok3, time.Hour); err != nil {
		t.Fatal(err)
	}
	if hc, err := s2.Enroll(tok3, newKey(t)); err != nil || hc.TunnelIP != "100.64.0.2" {
		t.Fatalf("enroll c: %+v %v", hc, err)
	}
}

func TestStateTokenRules(t *testing.T) {
	s, clk, _ := openState(t)
	k := newKey(t)
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"zero ttl", func() error { return s.AddToken("x", newToken(t), 0) }, nil},
		{"expired token", func() error {
			tok := newToken(t)
			if err := s.AddToken("x", tok, time.Minute); err != nil {
				return err
			}
			clk.t = clk.t.Add(time.Minute)
			_, err := s.Enroll(tok, k)
			return err
		}, enroll.ErrTokenExpired},
		{"replaced token", func() error {
			old, cur := newToken(t), newToken(t)
			if err := s.AddToken("y", old, time.Hour); err != nil {
				return err
			}
			if err := s.AddToken("y", cur, time.Hour); err != nil {
				return err
			}
			_, err := s.Enroll(old, k)
			return err
		}, enroll.ErrTokenInvalid},
		{"enrolled name", func() error {
			tok := newToken(t)
			if err := s.AddToken("z", tok, time.Hour); err != nil {
				return err
			}
			if _, err := s.Enroll(tok, k); err != nil {
				return err
			}
			return s.AddToken("z", newToken(t), time.Hour)
		}, ErrHubExists},
		{"key enrolled twice", func() error {
			tok := newToken(t)
			if err := s.AddToken("w", tok, time.Hour); err != nil {
				return err
			}
			_, err := s.Enroll(tok, k)
			return err
		}, ErrKeyEnrolled},
		{"revoke pending token", func() error {
			tok := newToken(t)
			if err := s.AddToken("p", tok, time.Hour); err != nil {
				return err
			}
			if _, err := s.Revoke("p"); err != nil {
				return err
			}
			_, err := s.Enroll(tok, newKey(t))
			return err
		}, enroll.ErrTokenInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if tt.want == nil {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestStateSaveFailureRollsBack(t *testing.T) {
	s, _, path := openState(t)
	s.path = filepath.Join(filepath.Dir(path), "missing", "state.json")
	tok := newToken(t)
	if err := s.AddToken("a", tok, time.Hour); err == nil {
		t.Fatal("save into a missing directory succeeded")
	}
	s.path = path
	if _, err := s.Enroll(tok, newKey(t)); !errors.Is(err, enroll.ErrTokenInvalid) {
		t.Fatalf("token survived a failed save: %v", err)
	}
}

func TestOpenStateErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct{ name, body string }{
		{"junk", "{"},
		{"bad hash", `{"v":1,"hubs":[],"tokens":[{"hash":"zz","name":"a"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name+".json")
			if err := os.WriteFile(p, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenState(p, nil); err == nil {
				t.Fatal("bad state accepted")
			}
		})
	}
}

func TestFreeIPPoolFull(t *testing.T) {
	s, _, _ := openState(t)
	for ip := ServerIP.Next(); TunnelPrefix.Contains(ip); ip = ip.Next() {
		s.hubs[ip.String()] = HubRecord{Name: ip.String(), TunnelIP: ip.String()}
	}
	if _, err := s.freeIPLocked(); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("full pool: %v", err)
	}
}

func TestStateEnrollKnown(t *testing.T) {
	tests := []struct {
		name     string
		run      func(s *State, clk *clock, k [crypto.KeyLen]byte, used enroll.Token) (HubRecord, error)
		want     error
		wantName string
		wantPort int
	}{
		{"new code renames, takes the current port", func(s *State, _ *clock, k [crypto.KeyLen]byte, _ enroll.Token) (HubRecord, error) {
			tok := newToken(t)
			if err := s.AddToken("b", tok, time.Hour); err != nil {
				return HubRecord{}, err
			}
			if _, err := s.SetTunnelPort(51901); err != nil {
				return HubRecord{}, err
			}
			if h, err := s.EnrollKnown(tok, k); err != nil || h.Name != "b" {
				return h, err
			}
			return s.EnrollKnown(tok, k) // the hub repeats the code: consumed now
		}, enroll.ErrTokenInvalid, "b", 51901},
		{"used code ignored", func(s *State, _ *clock, k [crypto.KeyLen]byte, used enroll.Token) (HubRecord, error) {
			return s.EnrollKnown(used, k)
		}, enroll.ErrTokenInvalid, "a", 51900},
		{"expired code ignored", func(s *State, clk *clock, k [crypto.KeyLen]byte, _ enroll.Token) (HubRecord, error) {
			tok := newToken(t)
			if err := s.AddToken("b", tok, time.Minute); err != nil {
				return HubRecord{}, err
			}
			clk.t = clk.t.Add(time.Minute)
			return s.EnrollKnown(tok, k)
		}, enroll.ErrTokenExpired, "a", 51900},
		{"unknown key", func(s *State, _ *clock, _ [crypto.KeyLen]byte, _ enroll.Token) (HubRecord, error) {
			tok := newToken(t)
			if err := s.AddToken("b", tok, time.Hour); err != nil {
				return HubRecord{}, err
			}
			return s.EnrollKnown(tok, newKey(t))
		}, ErrHubNotFound, "a", 51900},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, clk, path := openState(t)
			if _, err := s.SetTunnelPort(51900); err != nil {
				t.Fatal(err)
			}
			k, used := newKey(t), newToken(t)
			if err := s.AddToken("a", used, time.Hour); err != nil {
				t.Fatal(err)
			}
			first, err := s.Enroll(used, k)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tc.run(s, clk, k, used); !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			s2, err := OpenState(path, clk.now)
			if err != nil {
				t.Fatal(err)
			}
			hubs := s2.Hubs()
			if len(hubs) != 1 || hubs[0].Name != tc.wantName || hubs[0].Port != tc.wantPort ||
				hubs[0].TunnelIP != first.TunnelIP || hubs[0].PublicKey != first.PublicKey {
				t.Fatalf("hubs after reopen %+v, want %q port %d", hubs, tc.wantName, tc.wantPort)
			}
		})
	}
}
