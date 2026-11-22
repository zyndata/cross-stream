package tun

import (
	"fmt"
	"sync"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// headroom is the offset wireguard-go needs in front of every packet
// (virtio-net header on Linux with offloads enabled).
const headroom = 16

// native adapts a wireguard-go tun.Device (batched I/O) to Device.
type native struct {
	dev  wgtun.Device
	name string
	mtu  int

	rmu   sync.Mutex // guards the read batch below
	bufs  [][]byte
	sizes []int
	next  int // index of the next unread packet in bufs
	count int // packets in the current batch

	wmu  sync.Mutex
	wbuf []byte
}

// Create creates and configures the TUN interface described by cfg: address,
// netmask, MTU and link up.
func Create(cfg Config) (Device, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	dev, name, err := openDevice(cfg)
	if err != nil {
		return nil, err
	}
	go func() { // wireguard-go needs its event channel drained
		for range dev.Events() {
		}
	}()
	n := dev.BatchSize()
	t := &native{dev: dev, name: name, mtu: cfg.MTU, sizes: make([]int, n), bufs: make([][]byte, n)}
	for i := range t.bufs {
		t.bufs[i] = make([]byte, headroom+cfg.MTU+64)
	}
	t.wbuf = make([]byte, headroom+cfg.MTU+64)
	return t, nil
}

func (t *native) Name() string { return t.name }
func (t *native) MTU() int     { return t.mtu }

func (t *native) Read(p []byte) (int, error) {
	t.rmu.Lock()
	defer t.rmu.Unlock()
	for t.next >= t.count {
		n, err := t.dev.Read(t.bufs, t.sizes, headroom)
		if err != nil {
			return 0, fmt.Errorf("tun read: %w", err)
		}
		t.next, t.count = 0, n
	}
	i := t.next
	t.next++
	if t.sizes[i] > len(p) {
		return 0, fmt.Errorf("tun read: packet %d bytes > buffer %d", t.sizes[i], len(p))
	}
	return copy(p, t.bufs[i][headroom:headroom+t.sizes[i]]), nil
}

func (t *native) Write(p []byte) (int, error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	if headroom+len(p) > len(t.wbuf) {
		t.wbuf = make([]byte, headroom+len(p))
	}
	n := copy(t.wbuf[headroom:], p)
	if _, err := t.dev.Write([][]byte{t.wbuf[:headroom+n]}, headroom); err != nil {
		return 0, fmt.Errorf("tun write: %w", err)
	}
	return n, nil
}

func (t *native) Close() error {
	if err := t.dev.Close(); err != nil {
		return fmt.Errorf("tun close: %w", err)
	}
	return nil
}
