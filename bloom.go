package hearsay

import (
	"encoding/binary"
	"math"
)

// bloom is a fixed-FPR bloom filter. Keys are uniform sha256-derived
// values, so the two hash functions are taken directly from the key
// bytes (Kirsch-Mitzenmacher double hashing).
type bloom struct {
	k    uint32
	bits []byte
}

const bloomFPR = 0.01

func newBloom(n int) *bloom {
	if n < 1 {
		n = 1
	}
	m := uint64(math.Ceil(-float64(n) * math.Log(bloomFPR) / (math.Ln2 * math.Ln2)))
	m = (m + 7) &^ 7
	k := max(uint32(math.Round(float64(m)/float64(n)*math.Ln2)), 1)
	return &bloom{k: k, bits: make([]byte, m/8)}
}

func (b *bloom) add(key Key) {
	h1 := binary.BigEndian.Uint64(key[0:8])
	h2 := binary.BigEndian.Uint64(key[8:16])
	m := uint64(len(b.bits)) * 8
	for i := uint64(0); i < uint64(b.k); i++ {
		p := (h1 + i*h2) % m
		b.bits[p/8] |= 1 << (p % 8)
	}
}

func (b *bloom) test(key Key) bool {
	h1 := binary.BigEndian.Uint64(key[0:8])
	h2 := binary.BigEndian.Uint64(key[8:16])
	m := uint64(len(b.bits)) * 8
	for i := uint64(0); i < uint64(b.k); i++ {
		p := (h1 + i*h2) % m
		if b.bits[p/8]&(1<<(p%8)) == 0 {
			return false
		}
	}
	return true
}
