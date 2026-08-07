package hearsay

import (
	"encoding/binary"
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

func TestValidBoolPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{3})
	for _, n := range []int{1, 2, 3, 4, 5, 8, 13, 40, 1000} {
		keys := make([]Key, n)
		for i := range keys {
			keys[i] = randKey(r)
		}
		if !validBoolPayload(encodeBoolPayload(keys), uint32(n)) {
			t.Fatalf("honest payload rejected for n=%d", n)
		}
	}

	honest := encodeBoolPayload([]Key{{1}, {2}, {3}})
	if validBoolPayload(honest, 4) {
		t.Fatal("wrong count accepted")
	}
	if validBoolPayload(honest[:len(honest)-1], 3) {
		t.Fatal("truncated payload accepted")
	}

	hugeK := append([]byte(nil), honest...)
	binary.BigEndian.PutUint32(hugeK, 1<<30)
	if validBoolPayload(hugeK, 3) {
		t.Fatal("forged hash count accepted")
	}

	saturated := append([]byte(nil), honest...)
	for i := 4; i < len(saturated); i++ {
		saturated[i] = 0xff
	}
	if validBoolPayload(saturated, 3) {
		t.Fatal("saturated filter accepted")
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
