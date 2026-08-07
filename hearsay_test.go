package hearsay

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testDomain struct {
	ns string
	vt ValueType
}

func (d testDomain) Namespace() string { return d.ns }

func (d testDomain) Encode(subject any) (Key, error) {
	return sha256.Sum256([]byte(subject.(string))), nil
}

func (d testDomain) ValueType() ValueType { return d.vt }

func (d testDomain) Decay(time.Duration) float64 { return 1 }

func (d testDomain) Verify(subject any, outcome any) Truth {
	switch o := outcome.(type) {
	case bool:
		if o {
			return Truth{Known: true, Value: 1}
		}
		return Truth{Known: true, Value: 0}
	case float64:
		return Truth{Known: true, Value: o}
	}
	return Truth{}
}

func remoteGen(t *testing.T, h *Hearsay, id ed25519.PrivateKey, ns string, seq uint64, subjects map[string]float64, vt ValueType) {
	t.Helper()
	d := testDomain{ns, vt}
	var payload []byte
	if vt == Bool {
		var keys []Key
		for s, v := range subjects {
			if v >= 0.5 {
				k, _ := d.Encode(s)
				keys = append(keys, k)
			}
		}
		payload = encodeBoolPayload(keys)
	} else {
		m := map[Key]float64{}
		for s, v := range subjects {
			k, _ := d.Encode(s)
			m[k] = v
		}
		payload = encodeScalarPayload(m)
	}
	g, err := BuildGeneration(id, ns, vt, seq, time.Now(), len(subjects), payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := g.MarshalBinary()
	if _, err := h.Ingest(raw); err != nil {
		t.Fatal(err)
	}
}

func TestGroundTruthWins(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Observe("ns.bool", "abc", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, id, _ := ed25519.GenerateKey(nil)
	remoteGen(t, h, id, "ns.bool", 1, map[string]float64{"other": 1}, Bool)

	a, err := h.Query("ns.bool", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Local || a.Confidence != 1 || a.Value != 1 {
		t.Fatalf("want authoritative local answer, got %+v", a)
	}
}

func TestQueryRemoteSources(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, a1, _ := ed25519.GenerateKey(nil)
	_, a2, _ := ed25519.GenerateKey(nil)
	_, a3, _ := ed25519.GenerateKey(nil)
	remoteGen(t, h, a1, "ns.bool", 1, map[string]float64{"x": 1}, Bool)
	remoteGen(t, h, a2, "ns.bool", 1, map[string]float64{"x": 1}, Bool)
	remoteGen(t, h, a3, "ns.bool", 1, map[string]float64{"y": 1}, Bool)

	a, err := h.Query("ns.bool", "x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Value != 1 || a.Sources != 2 || a.Local {
		t.Fatalf("got %+v", a)
	}
	if a.Confidence < 0.6 || a.Confidence > 0.7 {
		t.Fatalf("confidence = %v, want 2/3", a.Confidence)
	}
	if a.Coverage != 1 {
		t.Fatalf("coverage = %v", a.Coverage)
	}

	a, _ = h.Query("ns.bool", "zzz")
	if a.Sources != 0 || a.Confidence != 0 || a.Value != 0 {
		t.Fatalf("miss should be empty, got %+v", a)
	}
}

func TestReputationFeedback(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, liar, _ := ed25519.GenerateKey(nil)
	claims := map[string]float64{}
	for _, s := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		claims[s] = 1
	}
	remoteGen(t, h, liar, "ns.bool", 1, claims, Bool)

	feed := h.Feeds("ns.bool")[0]
	if w := h.rep.weight(feed); w != 0.5 {
		t.Fatalf("initial weight = %v", w)
	}
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		if err := h.Report("ns.bool", s, false); err != nil {
			t.Fatal(err)
		}
	}
	if w := h.rep.weight(feed); w >= 0.2 {
		t.Fatalf("weight after 5 disagreements = %v, want < 0.2", w)
	}

	a, _ := h.Query("ns.bool", "f")
	if a.Value != 1 {
		t.Fatalf("sole source still wins its own total, got %+v", a)
	}
	if o, ok := h.store.truth["ns.bool"][sha256.Sum256([]byte("a"))]; !ok || o.Value != 0 {
		t.Fatal("reported outcome not recorded as ground truth")
	}
}

func TestScalarQuery(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.scalar", Scalar})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, a1, _ := ed25519.GenerateKey(nil)
	_, a2, _ := ed25519.GenerateKey(nil)
	remoteGen(t, h, a1, "ns.scalar", 1, map[string]float64{"x": 0.8}, Scalar)
	remoteGen(t, h, a2, "ns.scalar", 1, map[string]float64{"x": 0.6}, Scalar)

	a, err := h.Query("ns.scalar", "x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Value < 0.69 || a.Value > 0.71 {
		t.Fatalf("weighted mean = %v, want 0.7", a.Value)
	}
	if a.Confidence != 1 {
		t.Fatalf("confidence = %v", a.Confidence)
	}
}

func TestPublishCycle(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir, testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := h.Publish("ns.bool"); g != nil || err != nil {
		t.Fatalf("publish with no truth: %v, %v", g, err)
	}
	h.Observe("ns.bool", "s1", 1, time.Now())
	h.Observe("ns.bool", "s2", 0, time.Now())
	g, err := h.Publish("ns.bool")
	if err != nil || g == nil {
		t.Fatalf("publish: %v, %v", g, err)
	}
	if g.Seq != 1 || g.Count != 1 {
		t.Fatalf("seq=%d count=%d, want 1 and 1", g.Seq, g.Count)
	}
	if g2, _ := h.Publish("ns.bool"); g2 != nil {
		t.Fatal("clean publish should be a no-op")
	}
	h.Observe("ns.bool", "s3", 1, time.Now())
	g3, _ := h.Publish("ns.bool")
	if g3 == nil || g3.Seq != 2 || g3.Count != 2 {
		t.Fatalf("got %+v", g3)
	}
	h.Close()

	h2, err := New(dir, testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	a, _ := h2.Query("ns.bool", "s1")
	if !a.Local || a.Value != 1 {
		t.Fatalf("truth lost across restart: %+v", a)
	}
	if g := h2.Latest("ns.bool", h2.self); g == nil || g.Seq != 2 {
		t.Fatal("own generations lost across restart")
	}
	if g, _ := h2.Publish("ns.bool"); g != nil {
		t.Fatal("restart with no new truth should not republish")
	}
}

func TestRetention(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, id, _ := ed25519.GenerateKey(nil)
	for seq := uint64(1); seq <= Retention+3; seq++ {
		remoteGen(t, h, id, "ns.bool", seq, map[string]float64{"x": 1}, Bool)
	}
	feed := h.Feeds("ns.bool")[0]
	gens := h.store.feeds["ns.bool"][feed]
	if len(gens) != Retention || gens[0].Seq != 4 {
		t.Fatalf("retained %d gens starting at %d", len(gens), gens[0].Seq)
	}
	_, id2, _ := ed25519.GenerateKey(nil)
	g, _ := BuildGeneration(id2, "ns.bool", Bool, 1, time.Now(), 1, encodeBoolPayload([]Key{{1}}))
	raw, _ := g.MarshalBinary()
	h.Ingest(raw)
	stale, _ := BuildGeneration(id2, "ns.bool", Bool, 1, time.Now(), 1, encodeBoolPayload([]Key{{2}}))
	rawStale, _ := stale.MarshalBinary()
	if _, err := h.Ingest(rawStale); err == nil {
		t.Fatal("stale seq accepted")
	}
}

func TestIngestRejectsFutureGeneration(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, id, _ := ed25519.GenerateKey(nil)
	d := testDomain{"ns.bool", Bool}
	key, _ := d.Encode("abc")
	payload := encodeBoolPayload([]Key{key})

	for _, tc := range []struct {
		name    string
		builtAt time.Time
		want    error
	}{
		{"within skew", time.Now().Add(maxSkew / 2), nil},
		{"beyond skew", time.Now().Add(2 * maxSkew), ErrFutureGeneration},
		{"far future", time.Now().Add(100 * 365 * 24 * time.Hour), ErrFutureGeneration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := BuildGeneration(id, "ns.bool", Bool, 1, tc.builtAt, 1, payload)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := g.MarshalBinary()
			if _, err := h.Ingest(raw); !errors.Is(err, tc.want) {
				t.Fatalf("Ingest = %v, want %v", err, tc.want)
			}
		})
	}
}

// A future BuiltAt would otherwise hold a source at the freshness clamp
// while honest ones decay, so it must not reach the query path at all.
func TestFutureGenerationCannotOutlastDecay(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, id, _ := ed25519.GenerateKey(nil)
	d := testDomain{"ns.bool", Bool}
	key, _ := d.Encode("abc")
	g, _ := BuildGeneration(id, "ns.bool", Bool, 1, time.Now().Add(50*365*24*time.Hour), 1, encodeBoolPayload([]Key{key}))
	raw, _ := g.MarshalBinary()
	h.Ingest(raw)

	a, err := h.Query("ns.bool", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if a.Sources != 0 || a.Confidence != 0 {
		t.Fatalf("future generation reached the query path: %+v", a)
	}
}

func TestForgetDropsFeed(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir, testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, id, _ := ed25519.GenerateKey(nil)
	remoteGen(t, h, id, "ns.bool", 1, map[string]float64{"abc": 1}, Bool)
	feed := hex.EncodeToString(id.Public().(ed25519.PublicKey))

	if a, _ := h.Query("ns.bool", "abc"); a.Sources != 1 {
		t.Fatalf("setup: sources = %d", a.Sources)
	}
	if err := h.Forget("ns.bool", feed); err != nil {
		t.Fatal(err)
	}
	if a, _ := h.Query("ns.bool", "abc"); a.Sources != 0 {
		t.Fatalf("forgotten feed still answers: %+v", a)
	}
	if got := h.Feeds("ns.bool"); len(got) != 0 {
		t.Fatalf("feeds = %v", got)
	}
	// The generations must be gone from disk too, or the next open
	// reloads them.
	if _, err := os.Stat(filepath.Join(dir, "feeds", feed, "ns.bool")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("generations still on disk: %v", err)
	}
}
