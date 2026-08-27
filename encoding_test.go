package hearsay

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"
)

func randKey(r *rand.ChaCha8) Key {
	var key Key
	r.Read(key[:])
	return key
}

func boolTestPayload(keys []Key, at time.Time) ([]byte, int) {
	observations := make(map[Key]Observation, len(keys))
	for _, key := range keys {
		observations[key] = Observation{Value: 1, At: at}
	}
	return encodeBoolPayload(observations, FreshnessPolicy{Bucket: time.Hour, MaxAge: 24 * time.Hour}, at)
}

func TestBoolPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{1})
	keys := make([]Key, 10000)
	for i := range keys {
		keys[i] = randKey(r)
	}
	now := time.Now()
	payload, count := boolTestPayload(keys, now)
	if count != len(keys) {
		t.Fatalf("count = %d", count)
	}
	for _, key := range keys {
		if _, _, ok := payloadLookup(Bool, payload, key); !ok {
			t.Fatal("false negative")
		}
	}
	falsePositives := 0
	for range 10000 {
		if _, _, ok := payloadLookup(Bool, payload, randKey(r)); ok {
			falsePositives++
		}
	}
	if falsePositives > 20 {
		t.Fatalf("false positive rate too high: %d/10000", falsePositives)
	}
}

func TestValidBoolPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{3})
	now := time.Now()
	for _, n := range []int{1, 2, 3, 4, 5, 8, 13, 40, 1000} {
		keys := make([]Key, n)
		for i := range keys {
			keys[i] = randKey(r)
		}
		payload, count := boolTestPayload(keys, now)
		if !validBoolPayload(payload, uint32(count), now) {
			t.Fatalf("honest payload rejected for n=%d", n)
		}
	}

	honest, count := boolTestPayload([]Key{{1}, {2}, {3}}, now)
	if validBoolPayload(honest, uint32(count+1), now) {
		t.Fatal("wrong count accepted")
	}
	if validBoolPayload(honest[:len(honest)-1], uint32(count), now) {
		t.Fatal("truncated payload accepted")
	}

	hugeHashes := append([]byte(nil), honest...)
	binary.BigEndian.PutUint32(hugeHashes[16:], 1<<30)
	if validBoolPayload(hugeHashes, uint32(count), now) {
		t.Fatal("forged hash count accepted")
	}

	saturated := append([]byte(nil), honest...)
	for i := boolPayloadHeaderLen + boolBucketHeaderLen; i < len(saturated); i++ {
		saturated[i] = 0xff
	}
	if validBoolPayload(saturated, uint32(count), now) {
		t.Fatal("saturated filter accepted")
	}
}

func TestScalarPayload(t *testing.T) {
	r := rand.NewChaCha8([32]byte{2})
	rng := rand.New(r)
	now := time.Now()
	observations := map[Key]Observation{}
	for range 1000 {
		observations[randKey(r)] = Observation{Value: rng.Float64(), At: now}
	}
	key := randKey(r)
	observations[key] = Observation{Value: 0.1 + 0.2, At: now}
	payload, count := encodeScalarPayload(observations, FreshnessPolicy{MaxAge: 24 * time.Hour}, now)
	if !validScalarPayload(payload, uint32(count), now) {
		t.Fatal("honest scalar payload rejected")
	}
	for key, want := range observations {
		got, observedAt, ok := payloadLookup(Scalar, payload, key)
		if !ok || got != want.Value || observedAt.Unix() != now.Unix() {
			t.Fatalf("lookup = %v, %v, %v; want %+v", got, observedAt, ok, want)
		}
	}
	if _, _, ok := payloadLookup(Scalar, payload, randKey(r)); ok {
		t.Fatal("hit for absent key")
	}
}

func TestPayloadDropsExpiredObservations(t *testing.T) {
	now := time.Now()
	policy := FreshnessPolicy{Bucket: time.Hour, MaxAge: 24 * time.Hour}
	observations := map[Key]Observation{
		{1}: {Value: 1, At: now.Add(-25 * time.Hour)},
		{2}: {Value: 1, At: now.Add(-23 * time.Hour)},
	}
	payload, count := encodeBoolPayload(observations, policy, now)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, _, ok := payloadLookup(Bool, payload, Key{1}); ok {
		t.Fatal("expired observation encoded")
	}
	if _, observedAt, ok := payloadLookup(Bool, payload, Key{2}); !ok || observedAt.After(now.Add(-23*time.Hour)) {
		t.Fatalf("fresh observation missing or refreshed: %v, %v", observedAt, ok)
	}
}
