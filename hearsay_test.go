package hearsay

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type testDomain struct {
	ns string
	vt ValueType
}

func TestNewWithoutDomainsIsLocalAndInactive(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if len(h.Domains()) != 0 {
		t.Fatalf("domains = %v", h.Domains())
	}
}

func (d testDomain) Namespace() string { return d.ns }

func (d testDomain) Encode(subject any) (Key, error) {
	return sha256.Sum256([]byte(subject.(string))), nil
}

func (d testDomain) ValueType() ValueType { return d.vt }

func (d testDomain) Decay(time.Duration) float64 { return 1 }
func (d testDomain) Freshness() FreshnessPolicy {
	if d.vt == Bool {
		return FreshnessPolicy{Bucket: time.Hour, MaxAge: 24 * time.Hour}
	}
	return FreshnessPolicy{MaxAge: 24 * time.Hour}
}

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
	remoteGenAt(t, h, id, ns, seq, subjects, vt, time.Now())
}

func remoteGenAt(t *testing.T, h *Hearsay, id ed25519.PrivateKey, ns string, seq uint64, subjects map[string]float64, vt ValueType, now time.Time) {
	t.Helper()
	d := testDomain{ns, vt}
	observations := make(map[Key]Observation, len(subjects))
	for subject, value := range subjects {
		key, _ := d.Encode(subject)
		observations[key] = Observation{Value: value, At: now}
	}
	var payload []byte
	var count int
	if vt == Bool {
		payload, count = encodeBoolPayload(observations, d.Freshness(), now)
	} else {
		payload, count = encodeScalarPayload(observations, d.Freshness(), now)
	}
	g, err := BuildGeneration(id, ns, vt, seq, now, count, payload)
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
	if !a.Known || !a.Local || a.Support != 1 || a.Value != 1 {
		t.Fatalf("want authoritative local answer, got %+v", a)
	}
}

func TestInvalidNamespace(t *testing.T) {
	if _, err := New(t.TempDir(), testDomain{"../bad", Bool}); err == nil {
		t.Fatal("invalid namespace accepted")
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
	if a.Support < 0.6 || a.Support > 0.7 {
		t.Fatalf("support = %v, want 2/3", a.Support)
	}
	if a.Coverage != 1 {
		t.Fatalf("coverage = %v", a.Coverage)
	}

	a, _ = h.Query("ns.bool", "zzz")
	if a.Known || a.Sources != 0 || a.Support != 0 || a.Value != 0 {
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
	if w := h.rep.weight(feed); w != probationWeight {
		t.Fatalf("initial weight = %v", w)
	}
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		if err := h.Report("ns.bool", s, false); err != nil {
			t.Fatal(err)
		}
	}
	if w := h.rep.weight(feed); w > 0.01 {
		t.Fatalf("weight after 5 disagreements = %v, want <= 0.01", w)
	}

	a, _ := h.Query("ns.bool", "f")
	if a.Value != 1 {
		t.Fatalf("sole source still wins its own total, got %+v", a)
	}
	if a.EvidenceWeight > 0.01 {
		t.Fatalf("failed source retained too much absolute influence: %+v", a)
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
	if !a.Known || a.Support != 1 {
		t.Fatalf("support = %v", a.Support)
	}
}

func TestExpiredLocalObservationIsUnknown(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Observe("ns.bool", "old", 1, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	answer, err := h.Query("ns.bool", "old")
	if err != nil {
		t.Fatal(err)
	}
	if answer.Known || answer.Local {
		t.Fatalf("expired local observation answered: %+v", answer)
	}
}

func TestQueryPair(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.positive", Bool}, testDomain{"ns.negative", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Observe("ns.positive", "x", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.Observe("ns.negative", "x", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	paired, err := h.QueryPair("ns.positive", "ns.negative", "x")
	if err != nil {
		t.Fatal(err)
	}
	if paired.State != EvidencePositive || !paired.Positive.Known || paired.Negative.Known {
		t.Fatalf("positive pair = %+v", paired)
	}
	if err := h.Observe("ns.positive", "x", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.Observe("ns.negative", "x", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	paired, err = h.QueryPair("ns.positive", "ns.negative", "x")
	if err != nil {
		t.Fatal(err)
	}
	if paired.State != EvidenceNegative || paired.Positive.Known || !paired.Negative.Known {
		t.Fatalf("negative pair = %+v", paired)
	}
}

func TestPublishPrunesExpiredTruth(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Observe("ns.bool", "old", 1, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	generation, err := h.Publish("ns.bool")
	if err != nil {
		t.Fatal(err)
	}
	if generation == nil || generation.Count != 0 {
		t.Fatalf("expired truth generation = %+v", generation)
	}
	if len(h.store.truth["ns.bool"]) != 0 {
		t.Fatal("expired truth remains in the store")
	}
}

func TestRepublishDoesNotRefreshObservationAge(t *testing.T) {
	dir := t.TempDir()
	publisher, err := New(filepath.Join(dir, "publisher"), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	consumer, err := New(filepath.Join(dir, "consumer"), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	oldAt := time.Now().Add(-23 * time.Hour)
	if err := publisher.Observe("ns.bool", "old", 1, oldAt); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish("ns.bool"); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Observe("ns.bool", "new", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	generation, err := publisher.Publish("ns.bool")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := generation.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Ingest(raw); err != nil {
		t.Fatal(err)
	}
	answer, err := consumer.Query("ns.bool", "old")
	if err != nil {
		t.Fatal(err)
	}
	if !answer.Known || answer.Age < 22*time.Hour {
		t.Fatalf("old claim was refreshed: %+v", answer)
	}
}

func TestNewestSnapshotWithdrawsOlderClaim(t *testing.T) {
	h, err := New(t.TempDir(), testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, publisher, _ := ed25519.GenerateKey(nil)
	remoteGen(t, h, publisher, "ns.bool", 1, map[string]float64{"old": 1}, Bool)
	remoteGen(t, h, publisher, "ns.bool", 2, map[string]float64{"new": 1}, Bool)
	answer, err := h.Query("ns.bool", "old")
	if err != nil {
		t.Fatal(err)
	}
	if answer.Known {
		t.Fatalf("withdrawn claim survived in an older snapshot: %+v", answer)
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
	now := time.Now()
	payload, count := boolTestPayload([]Key{{1}}, now)
	g, _ := BuildGeneration(id2, "ns.bool", Bool, 1, now, count, payload)
	raw, _ := g.MarshalBinary()
	h.Ingest(raw)
	stalePayload, staleCount := boolTestPayload([]Key{{2}}, now)
	stale, _ := BuildGeneration(id2, "ns.bool", Bool, 1, now, staleCount, stalePayload)
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
	payload, count := boolTestPayload([]Key{key}, time.Now())

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
			g, err := BuildGeneration(id, "ns.bool", Bool, 1, tc.builtAt, count, payload)
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
	payload, count := boolTestPayload([]Key{key}, time.Now())
	g, _ := BuildGeneration(id, "ns.bool", Bool, 1, time.Now().Add(50*365*24*time.Hour), count, payload)
	raw, _ := g.MarshalBinary()
	h.Ingest(raw)

	a, err := h.Query("ns.bool", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if a.Known || a.Sources != 0 || a.Support != 0 {
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

func TestPruneFeedsBeforeDropsStaleRemoteData(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir, testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, id, _ := ed25519.GenerateKey(nil)
	remoteGenAt(t, h, id, "ns.bool", 1, map[string]float64{"abc": 1}, Bool, time.Now().Add(-48*time.Hour))
	feed := hex.EncodeToString(id.Public().(ed25519.PublicKey))

	removed, err := h.PruneFeedsBefore("ns.bool", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != feed || len(h.Feeds("ns.bool")) != 0 {
		t.Fatalf("removed = %v, feeds = %v", removed, h.Feeds("ns.bool"))
	}
	if _, err := os.Stat(filepath.Join(dir, "feeds", feed, "ns.bool")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale feed remains on disk: %v", err)
	}
}

func TestRetainedFeedLimitKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir, testDomain{"ns.bool", Bool})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now()
	var feeds []string
	for i := range 3 {
		_, id, _ := ed25519.GenerateKey(nil)
		remoteGenAt(t, h, id, "ns.bool", 1, map[string]float64{"abc": 1}, Bool, now.Add(time.Duration(i-3)*time.Hour))
		feeds = append(feeds, hex.EncodeToString(id.Public().(ed25519.PublicKey)))
	}
	if err := h.store.limitFeeds("ns.bool", h.self, 2); err != nil {
		t.Fatal(err)
	}
	if got := h.Feeds("ns.bool"); len(got) != 2 || slices.Contains(got, feeds[0]) {
		t.Fatalf("feeds = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "feeds", feeds[0], "ns.bool")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("oldest feed remains on disk: %v", err)
	}
}
