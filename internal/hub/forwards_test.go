package hub

import (
	"errors"
	"net/netip"
	"testing"
)

func TestNormForwards(t *testing.T) {
	lan := []netip.Prefix{netip.MustParsePrefix("10.42.0.1/24")}
	ok := []ForwardRule{{Proto: " TCP ", Port: 8080, Target: "AA-BB-CC-00-11-22", Name: " nas "}, {Proto: "udp", Port: 8080, Target: "10.42.0.20"}}
	got, err := normForwards(ok, lan)
	if err != nil || got[0].Proto != "tcp" || got[0].Target != "aa:bb:cc:00:11:22" || got[0].Name != "nas" || got[1].Target != "10.42.0.20" {
		t.Fatalf("normalized %+v %v", got, err)
	}
	bad := []struct {
		name string
		r    ForwardRule
	}{
		{"proto", ForwardRule{Proto: "icmp", Port: 8080, Target: "10.42.0.20"}},
		{"low port", ForwardRule{Proto: "tcp", Port: 80, Target: "10.42.0.20"}},
		{"high port", ForwardRule{Proto: "tcp", Port: 50000, Target: "10.42.0.20"}},
		{"off lan", ForwardRule{Proto: "tcp", Port: 8080, Target: "192.168.1.5"}},
		{"garbage target", ForwardRule{Proto: "tcp", Port: 8080, Target: "nas"}},
	}
	for _, tt := range bad {
		if _, err := normForwards([]ForwardRule{tt.r}, lan); !errors.Is(err, ErrForward) {
			t.Errorf("%s: err %v", tt.name, err)
		}
	}
	if _, err := normForwards([]ForwardRule{ok[0], ok[0]}, lan); err == nil {
		t.Error("duplicate accepted")
	}
	many := make([]ForwardRule, MaxForwards+1)
	for i := range many {
		many[i] = ForwardRule{Proto: "tcp", Port: uint16(2000 + i), Target: "10.42.0.20"}
	}
	if _, err := normForwards(many, lan); err == nil {
		t.Error("too many accepted")
	}
}

func TestForwardsSetAndList(t *testing.T) {
	d, _, _ := startHubWithRelay(t)
	if _, err := d.ForwardsSet([]ForwardRule{{Proto: "tcp", Port: 8080, Target: "10.42.0.20"}}); err == nil {
		t.Fatal("target outside the hub LAN (127.0.0.0/8 here) accepted")
	}
	info, err := d.ForwardsSet([]ForwardRule{{Proto: "tcp", Port: 8080, Target: "127.0.0.20"},
		{Proto: "udp", Port: 41820, Target: "aa:bb:cc:00:11:22"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Server != "127.0.0.1" || len(info.Forwards) != 2 || info.Forwards[0].Client != "127.0.0.20" ||
		info.Forwards[0].State != FwdNotOnline {
		t.Fatalf("forwards %+v", info)
	}
	var saved []ForwardRule
	if ok, err := loadJSON(d.cfg.Dir, ForwardsFile, &saved); !ok || err != nil || len(saved) != 2 {
		t.Fatalf("forwards.json %+v %v %v", saved, ok, err)
	}
	if err := d.loadForwards(); err != nil || len(d.ForwardsList().Forwards) != 2 {
		t.Fatalf("reload: %v", err)
	}
}
