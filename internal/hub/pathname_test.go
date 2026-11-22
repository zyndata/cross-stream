package hub

import (
	"net/netip"
	"testing"

	"github.com/zyndata/cross-stream/internal/wire"
)

func TestPathNames(t *testing.T) {
	names := map[uint8]string{1: "Telefon Ani", 2: ""}
	c := &Client{PathName: func(id uint8) string { return names[id] }}
	c.AddPath(1, netip.MustParseAddrPort("10.0.0.1:1"), nil, wire.PathConfig{})
	c.AddPath(2, netip.MustParseAddrPort("10.0.0.2:1"), nil, wire.PathConfig{})
	got := func(full bool) map[uint8]string {
		out := map[uint8]string{}
		for _, f := range c.pathNames(full) {
			p := f.(*wire.PathName)
			out[p.PathID] = p.Name
		}
		return out
	}
	steps := []struct {
		name   string
		change func()
		full   bool
		want   map[uint8]string
	}{
		{"first: named paths only", nil, false, map[uint8]string{1: "Telefon Ani"}},
		{"unchanged: nothing", nil, false, map[uint8]string{}},
		{"full: all named again", nil, true, map[uint8]string{1: "Telefon Ani"}},
		{"renamed", func() { names[1] = "Ania" }, false, map[uint8]string{1: "Ania"}},
		{"name cleared is sent", func() { names[1] = "" }, false, map[uint8]string{1: ""}},
		{"long name truncated", func() { names[2] = "0123456789012345678901234567890123456789" }, false,
			map[uint8]string{2: "01234567890123456789012345678901"}},
	}
	for _, st := range steps {
		if st.change != nil {
			st.change()
		}
		g := got(st.full)
		if len(g) != len(st.want) {
			t.Fatalf("%s: got %v, want %v", st.name, g, st.want)
		}
		for id, n := range st.want {
			if g[id] != n {
				t.Fatalf("%s: got %v, want %v", st.name, g, st.want)
			}
		}
	}
	if (&Client{}).pathNames(true) != nil {
		t.Fatal("no PathName func: frames sent")
	}
}
