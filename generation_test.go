package hearsay

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestGenerationRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	payload := encodeBoolPayload([]Key{{1}, {2}, {3}})
	g, err := BuildGeneration(priv, "debrid.realdebrid.cached", Bool, 7, time.Now(), 3, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := g.MarshalBinary()
	got, err := ParseGeneration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != g.Namespace || got.Seq != 7 || got.Type != Bool || got.Count != 3 {
		t.Fatalf("fields mismatch: %+v", got)
	}
	if !got.BuiltAt.Equal(g.BuiltAt) {
		t.Fatalf("built at %v, want %v", got.BuiltAt, g.BuiltAt)
	}
	if got.FeedID() != g.FeedID() {
		t.Fatal("feed id mismatch")
	}
	if _, ok := payloadLookup(Bool, got.Payload, Key{2}); !ok {
		t.Fatal("payload lost")
	}
}

func TestGenerationHostileBloom(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)

	// Correct dimensions for the declared count, but every bit set:
	// would match all subjects.
	saturated := encodeBoolPayload([]Key{{1}, {2}, {3}})
	for i := 4; i < len(saturated); i++ {
		saturated[i] = 0xff
	}
	// Hash count far beyond what the count derives: CPU cost per
	// lookup is proportional to it.
	hugeK := encodeBoolPayload([]Key{{1}, {2}, {3}})
	binary.BigEndian.PutUint32(hugeK, 1<<30)

	for name, payload := range map[string][]byte{"saturated": saturated, "hugeK": hugeK} {
		g, err := BuildGeneration(priv, "ns.test", Bool, 1, time.Now(), 3, payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := g.MarshalBinary()
		if _, err := ParseGeneration(raw); !errors.Is(err, ErrInvalidGeneration) {
			t.Fatalf("%s payload accepted despite valid signatures", name)
		}
	}
}

func TestGenerationTamper(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	g, _ := BuildGeneration(priv, "ns.test", Scalar, 1, time.Now(), 1, encodeScalarPayload(map[Key]float64{{9}: 0.5}))
	raw, _ := g.MarshalBinary()

	for _, i := range []int{5, len(raw) / 2, len(raw) - 100} {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0xff
		if _, err := ParseGeneration(bad); !errors.Is(err, ErrInvalidGeneration) {
			t.Fatalf("tamper at %d accepted", i)
		}
	}
	if _, err := ParseGeneration(raw[:len(raw)-1]); !errors.Is(err, ErrInvalidGeneration) {
		t.Fatal("truncated blob accepted")
	}
}
