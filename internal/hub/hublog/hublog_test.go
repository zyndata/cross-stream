package hublog

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRing(t *testing.T) {
	r := NewRing(3)
	if got := r.Lines(); len(got) != 0 {
		t.Fatalf("empty ring: %q", got)
	}
	for i := range 2 {
		_, _ = fmt.Fprintf(r, "line %d\n", i)
	}
	if got := strings.Join(r.Lines(), ","); got != "line 0,line 1" {
		t.Fatalf("got %q", got)
	}
	_, _ = io.WriteString(r, "line 2\nline 3\n") // two lines in one write
	if got := strings.Join(r.Lines(), ","); got != "line 1,line 2,line 3" {
		t.Fatalf("after wrap: %q", got)
	}
	log := slog.New(slog.NewTextHandler(r, nil))
	log.Info("hotspot up", "psk", "geheim123")
	last := r.Redacted()[2]
	if !strings.Contains(last, `msg="hotspot up"`) || strings.Contains(last, "geheim123") {
		t.Fatalf("slog line %q", last)
	}
}

func TestRedact(t *testing.T) {
	key := "q83vEjRWeJASNFZ4kBI0VniQEjRWeJASNFZ4kBI0Vng="
	tests := []struct{ in, want string }{
		{`level=INFO msg=x psk=hunter22 ssid=CrossStream`, `level=INFO msg=x psk=[redacted] ssid=CrossStream`},
		{`msg=x wifi_password="a b c" n=1`, `msg=x wifi_password=[redacted] n=1`},
		{`msg=x pair_token=abc enroll_code=XS1.eyJh`, `msg=x pair_token=[redacted] enroll_code=[redacted]`},
		{`msg="paste XS1.eyJhYmMi.def= here" b=XSR1.AAAA`, `msg="paste XS1.[redacted] here" b=XSR1.[redacted]`},
		{`msg=x key=` + key, `msg=x key=q83vEj…`},
		{`msg=client mac=a4:5e:60:12:34:56`, `msg=client mac=a4:5e:60:xx:xx:xx`},
		{`msg=client mac=A4-5E-60-12-34-56`, `msg=client mac=A4-5E-60:xx:xx:xx`},
		{`from=203.0.113.77:51900 ep=udp://198.51.100.4:443`, `from=203.0.x.x:51900 ep=udp://198.51.x.x:443`},
		{`lan=10.43.0.11 tun=100.64.0.2 lo=127.0.0.1 ll=169.254.1.1 g=192.168.1.1`, `lan=10.43.0.11 tun=100.64.0.2 lo=127.0.0.1 ll=169.254.1.1 g=192.168.1.1`},
		{`time=2026-11-16T10:11:12.345+01:00 level=INFO msg=ok`, `time=2026-11-16T10:11:12.345+01:00 level=INFO msg=ok`},
		{`msg=x n=999.1.2.3`, `msg=x n=999.1.2.3`},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

func TestRedactKeyStartingWithPlusOrSlash(t *testing.T) {
	for _, key := range []string{
		"+bcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ=",
		"/bcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ=",
		"AbcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ=",
	} {
		got := Redact("relay connected key=" + key + " path=3")
		if strings.Contains(got, key[8:]) || !strings.Contains(got, "key="+key[:6]+"…") || !strings.HasSuffix(got, " path=3") {
			t.Errorf("key %q: redacted line %q", key[:6], got)
		}
	}
}
