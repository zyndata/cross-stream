package tun

import (
	"fmt"
	"sync"
)

// Pipe returns two connected in-memory Devices: a packet written to one is
// read from the other. Used by tests in place of a kernel TUN interface.
func Pipe(nameA, nameB string, mtu int) (Device, Device) {
	ab := make(chan []byte, 256)
	ba := make(chan []byte, 256)
	done := make(chan struct{})
	once := &sync.Once{}
	a := &pipeEnd{name: nameA, mtu: mtu, in: ba, out: ab, done: done, once: once}
	b := &pipeEnd{name: nameB, mtu: mtu, in: ab, out: ba, done: done, once: once}
	return a, b
}

type pipeEnd struct {
	name string
	mtu  int
	in   <-chan []byte
	out  chan<- []byte
	done chan struct{}
	once *sync.Once
}

func (p *pipeEnd) Name() string { return p.name }
func (p *pipeEnd) MTU() int     { return p.mtu }

func (p *pipeEnd) Read(b []byte) (int, error) {
	select {
	case pkt := <-p.in:
		if len(pkt) > len(b) {
			return 0, fmt.Errorf("tun pipe read: packet %d bytes > buffer %d", len(pkt), len(b))
		}
		return copy(b, pkt), nil
	case <-p.done:
		return 0, ErrClosed
	}
}

// Write copies b to the peer. A full queue drops the packet, like a real TUN
// under load.
func (p *pipeEnd) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, ErrClosed
	default:
	}
	if len(b) > p.mtu {
		return 0, fmt.Errorf("tun pipe write: packet %d bytes > MTU %d", len(b), p.mtu)
	}
	select {
	case p.out <- append([]byte(nil), b...):
	default:
	}
	return len(b), nil
}

// Close closes both ends.
func (p *pipeEnd) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
