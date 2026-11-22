package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d1 := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	d2 := d1.AddDate(0, 0, 1)
	s.Add("r1", "anna", 100, 1000, d1)
	s.Add("r1", "", 50, 500, d1) // name kept
	s.Add("r1", "anna", 1, 2, d2)
	s.Add("r2", "bob", 10_000, 0, d2)
	s.Add("r3", "zero", 0, 0, d2) // nothing counted
	s.AddFailover(d2)
	s.AddFailover(d2)
	if err := s.Save(d2); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Get("", "", "")
	if len(rep.Relays) != 2 || rep.Relays[0].Name != "bob" || rep.Failovers != 2 || len(rep.FailoverDays) != 1 {
		t.Fatalf("report %+v", rep)
	}
	a := rep.Relays[1]
	if a.Up != 151 || a.Down != 1502 || len(a.Days) != 2 || a.Days[0].Date != "2026-11-13" || a.Days[0].Up != 150 {
		t.Fatalf("anna %+v", a)
	}
	// Day range and relay filter (by id or name).
	if r := s.Get("anna", "2026-11-14", "2026-11-14"); len(r.Relays) != 1 || r.Relays[0].Up != 1 {
		t.Fatalf("filtered %+v", r)
	}
	if r := s.Get("r2", "", "2026-11-13"); len(r.Relays) != 0 || r.Failovers != 0 {
		t.Fatalf("empty range %+v", r)
	}

	// Days older than KeepDays are pruned on save.
	late := d2.AddDate(0, 0, KeepDays)
	s.Add("r2", "bob", 1, 1, late)
	if err := s.Save(late); err != nil {
		t.Fatal(err)
	}
	rep = s.Get("", "", "")
	if len(rep.Relays) != 2 || rep.Relays[0].Up != 10_001 || rep.Relays[1].Up != 1 || rep.Failovers != 2 { // the 13th is gone
		t.Fatalf("after prune %+v", rep)
	}
	if err := s.Save(late); err != nil { // nothing changed: no write
		t.Fatal(err)
	}
}

func TestOpenErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
	if err := os.WriteFile(path, []byte(`{"v":1,"relays":{"r":{"name":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Add("r", "x", 1, 1, time.Now()) // nil maps filled in
}
