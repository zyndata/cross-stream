package relay

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
)

func TestHandleBinder(t *testing.T) {
	errBind := errors.New("bind failed")
	tests := []struct {
		name       string
		wifi, cell int64
		bindErr    error
		n          Network
		wantHandle int64
		wantErr    error
	}{
		{name: "no wifi", cell: 7, n: NetWiFi, wantErr: ErrNoNetwork},
		{name: "no cellular", wifi: 5, n: NetCellular, wantErr: ErrNoNetwork},
		{name: "wifi", wifi: 5, cell: 7, n: NetWiFi, wantHandle: 5},
		{name: "cellular", wifi: 5, cell: 7, n: NetCellular, wantHandle: 7},
		{name: "platform error", wifi: 5, n: NetWiFi, bindErr: errBind, wantHandle: 5, wantErr: errBind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHandle int64
			var gotFD uintptr
			b := NewHandleBinder(func(h int64, fd uintptr) error { gotHandle, gotFD = h, fd; return tt.bindErr })
			b.SetNetwork(NetWiFi, tt.wifi)
			b.SetNetwork(NetCellular, tt.cell)
			err := b.Bind(tt.n, 42)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if gotHandle != tt.wantHandle {
				t.Fatalf("handle = %d, want %d", gotHandle, tt.wantHandle)
			}
			if tt.wantHandle != 0 && gotFD != 42 {
				t.Fatalf("fd = %d, want 42", gotFD)
			}
		})
	}
}

func TestHandleBinderSetNetwork(t *testing.T) {
	b := NewHandleBinder(nil)
	steps := []struct {
		n       Network
		h       int64
		changed bool
	}{
		{NetCellular, 9, true}, {NetCellular, 9, false}, {NetWiFi, 9, true}, {NetCellular, 0, true}, {NetWiFi, 3, true},
	}
	for i, s := range steps {
		if got := b.SetNetwork(s.n, s.h); got != s.changed {
			t.Fatalf("step %d: changed = %v, want %v", i, got, s.changed)
		}
		if b.Handle(s.n) != s.h {
			t.Fatalf("step %d: handle = %d, want %d", i, b.Handle(s.n), s.h)
		}
	}
	if err := b.Bind(NetWiFi, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("non-Android bind: %v, want ErrNotSupported", err)
	}
}

// A relay without a Wi-Fi network retries; once the network is reported,
// Reconnect skips the backoff wait.
func TestRelayReconnectAfterNetwork(t *testing.T) {
	h := newFakeHub(t)
	b := NewHandleBinder(func(int64, uintptr) error { return nil })
	b.SetNetwork(NetCellular, 2)
	r, _ := start(t, Config{Hub: h.payload(), Binder: b})
	time.Sleep(1200 * time.Millisecond) // two failed attempts, now waiting 2 s
	b.SetNetwork(NetWiFi, 1)
	r.Reconnect()
	select {
	case c := <-h.conns:
		_ = c.Close()
	case <-time.After(700 * time.Millisecond):
		t.Fatal("Reconnect did not skip the retry wait")
	}
}

// Reconnect on a live session drops it and connects again at once.
func TestRelayReconnectLive(t *testing.T) {
	h := newFakeHub(t)
	r, _ := start(t, Config{Hub: h.payload()})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port})
	deadline := time.Now().Add(3 * time.Second)
	for !r.Stats().Connected {
		if time.Now().After(deadline) {
			t.Fatal("relay did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Reconnect()
	select {
	case c2 := <-h.conns:
		t.Cleanup(func() { _ = c2.Close() })
		recvType(t, c2, control.TypeHello)
	case <-time.After(700 * time.Millisecond):
		t.Fatal("no new connection after Reconnect")
	}
}
