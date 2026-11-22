package crypto

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func cipherPair(t testing.TB) (*DataCipher, *DataCipher) {
	t.Helper()
	hk, sk := handshake(t, mustKeypair(t), mustKeypair(t))
	return NewDataCipher(hk.Send), NewDataCipher(sk.Recv)
}

func TestDataCipherSealOpen(t *testing.T) {
	send, recv := cipherPair(t)
	hdr := []byte{3, 0, 0, 0, 1, 2, 0, 0, 0, 0, 0, 0, 0, 7}
	pt := bytes.Repeat([]byte{0x42}, 1400)
	ct := send.Seal(hdr, hdr, pt, 7) // header + ciphertext in one buffer
	if len(ct) != len(hdr)+len(pt)+TagLen {
		t.Fatalf("len %d", len(ct))
	}
	tests := []struct {
		name    string
		hdr, ct []byte
		counter uint64
		ok      bool
	}{
		{"valid", hdr, ct[len(hdr):], 7, true},
		{"wrong counter", hdr, ct[len(hdr):], 8, false},
		{"header changed", append([]byte{4}, hdr[1:]...), ct[len(hdr):], 7, false},
		{"ciphertext changed", hdr, flip(ct[len(hdr):], 0), 7, false},
		{"tag changed", hdr, flip(ct[len(hdr):], len(ct)-len(hdr)-1), 7, false},
		{"truncated", hdr, ct[len(hdr) : len(ct)-1], 7, false},
		{"too short for tag", hdr, ct[len(hdr) : len(hdr)+5], 7, false},
		{"reserved counter", hdr, ct[len(hdr):], MaxCounter + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recv.Open(nil, tt.hdr, tt.ct, tt.counter)
			if tt.ok {
				if err != nil || !bytes.Equal(got, pt) {
					t.Fatalf("open: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrOpen) {
				t.Fatalf("err %v, want ErrOpen", err)
			}
		})
	}
}

func flip(b []byte, i int) []byte {
	c := bytes.Clone(b)
	c[i] ^= 0x80
	return c
}

func TestNextCounter(t *testing.T) {
	send, _ := cipherPair(t)
	for want := uint64(0); want < 3; want++ {
		if n, err := send.NextCounter(); err != nil || n != want {
			t.Fatalf("got %d %v, want %d", n, err, want)
		}
	}
	send.next.Store(MaxCounter)
	if n, err := send.NextCounter(); err != nil || n != MaxCounter {
		t.Fatalf("last counter: %d %v", n, err)
	}
	for range 3 {
		if _, err := send.NextCounter(); !errors.Is(err, ErrCounterExhausted) {
			t.Fatalf("err %v, want ErrCounterExhausted", err)
		}
	}
}

func TestNextCounterConcurrentUnique(t *testing.T) {
	send, _ := cipherPair(t)
	const workers, per = 8, 1000
	var mu sync.Mutex
	seen := make(map[uint64]bool, workers*per)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				n, err := send.NextCounter()
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[n] {
					t.Errorf("counter %d reused", n)
				}
				seen[n] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*per {
		t.Fatalf("%d counters", len(seen))
	}
}

// BenchmarkSealOpen1400 measures one seal plus one open of a 1400-byte packet.
func BenchmarkSealOpen1400(b *testing.B) {
	send, recv := cipherPair(b)
	hdr := make([]byte, 14)
	pt := make([]byte, 1400)
	ct := make([]byte, 0, 1400+TagLen)
	out := make([]byte, 0, 1400)
	b.SetBytes(1400)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		n := uint64(i)
		ct = send.Seal(ct[:0], hdr, pt, n)
		var err error
		if out, err = recv.Open(out[:0], hdr, ct, n); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
}
