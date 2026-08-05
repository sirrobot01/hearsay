package hearsay

import (
	"math/rand/v2"
	"testing"
)

func randKey(r *rand.ChaCha8) Key {
	var k Key
	r.Read(k[:])
	return k
}

func TestBoolPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{1})
	keys := make([]Key, 10000)
	for i := range keys {
		keys[i] = randKey(r)
	}
	p := encodeBoolPayload(keys)
	for _, k := range keys {
		if _, ok := payloadLookup(Bool, p, k); !ok {
			t.Fatal("false negative")
		}
	}
	fp := 0
	for range 10000 {
		if _, ok := payloadLookup(Bool, p, randKey(r)); ok {
			fp++
		}
	}
	if fp > 300 {
		t.Fatalf("false positive rate too high: %d/10000", fp)
	}
}

func TestScalarPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{2})
	rng := rand.New(r)
	m := map[Key]float64{}
	for range 1000 {
		m[randKey(r)] = rng.Float64()
	}
	k := randKey(r)
	m[k] = 0.1 + 0.2
	p := encodeScalarPayload(m)
	for key, want := range m {
		got, ok := payloadLookup(Scalar, p, key)
		if !ok || got != want {
			t.Fatalf("lookup = %v, %v; want %v", got, ok, want)
		}
	}
	if _, ok := payloadLookup(Scalar, p, randKey(r)); ok {
		t.Fatal("hit for absent key")
	}
}
