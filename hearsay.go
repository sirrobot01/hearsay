// Package hearsay shares observations about external state that no
// single participant can query cheaply, where those observations are
// byproducts of work already being done.
//
// Participants publish signed, generational snapshots of what they
// incidentally learned. Consumers merge snapshots from sources they
// choose, query them locally, and correct their own trust weightings
// from the outcomes of acting on them.
//
// Hearsay is a hint layer, never an authority. It answers "what do
// others believe, how strongly, and how recently" — not "what is
// true."
package hearsay

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Hearsay is the engine: local ground truth, retained remote feeds,
// per-source reputation, and the query path over all of it.
type Hearsay struct {
	id      ed25519.PrivateKey
	self    string
	domains map[string]Domain
	store   *store
	rep     *reputation
	metrics *metricsLedger

	mu    sync.Mutex
	dirty map[string]uint64
}

// New opens or creates a Hearsay instance in dir. The identity keypair
// is generated on first run; no accounts, no registration.
func New(dir string, domains ...Domain) (*Hearsay, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	id, err := loadIdentity(filepath.Join(dir, "identity.key"))
	if err != nil {
		return nil, err
	}
	st, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	rep, err := openReputation(filepath.Join(dir, "reputation.json"))
	if err != nil {
		return nil, err
	}
	metrics, err := openMetricsLedger(filepath.Join(dir, "metrics.json"))
	if err != nil {
		return nil, err
	}
	h := &Hearsay{
		id:      id,
		self:    hex.EncodeToString(id.Public().(ed25519.PublicKey)),
		domains: map[string]Domain{},
		store:   st,
		rep:     rep,
		metrics: metrics,
		dirty:   map[string]uint64{},
	}
	for _, d := range domains {
		ns := d.Namespace()
		if !validNamespace(ns) {
			return nil, fmt.Errorf("hearsay: invalid namespace %q", ns)
		}
		if _, dup := h.domains[ns]; dup {
			return nil, fmt.Errorf("hearsay: duplicate namespace %q", ns)
		}
		policy := d.Freshness()
		if policy.MaxAge <= 0 {
			return nil, fmt.Errorf("hearsay: %q must declare a positive max age", ns)
		}
		if d.ValueType() == Bool && (policy.Bucket < time.Second || policy.Bucket > policy.MaxAge || policy.Bucket%time.Second != 0) {
			return nil, fmt.Errorf("hearsay: %q has an invalid Boolean freshness bucket", ns)
		}
		h.domains[ns] = d
	}
	for ns, domain := range h.domains {
		if err := st.limitFeeds(ns, h.self, MaxRetainedFeedsPerNamespace); err != nil {
			return nil, err
		}
		pruned, err := st.pruneTruth(ns, time.Now().Add(-domain.Freshness().MaxAge))
		if err != nil {
			return nil, err
		}
		if pruned && h.Latest(ns, h.self) != nil {
			h.dirty[ns]++
		}
		// BuiltAt is truncated to seconds, so allow that much slack.
		if len(st.truth[ns]) > 0 && st.newestTruth(ns).After(h.latestBuilt(ns).Add(time.Second)) {
			h.dirty[ns]++
		}
	}
	return h, nil
}

func loadIdentity(path string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, seed, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("hearsay: malformed identity key %s", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func (s *store) newestTruth(ns string) time.Time {
	var t time.Time
	for _, o := range s.truth[ns] {
		if o.At.After(t) {
			t = o.At
		}
	}
	return t
}

func (h *Hearsay) latestBuilt(ns string) time.Time {
	if g := h.Latest(ns, h.self); g != nil {
		return g.BuiltAt
	}
	return time.Time{}
}

// Identity is the long-term public key other participants follow.
func (h *Hearsay) Identity() ed25519.PublicKey {
	return h.id.Public().(ed25519.PublicKey)
}

// PrivateIdentity is the long-term signing key. The transport needs
// it to sign BEP 44 pointer items.
func (h *Hearsay) PrivateIdentity() ed25519.PrivateKey {
	return h.id
}

func (h *Hearsay) Domain(ns string) (Domain, bool) {
	d, ok := h.domains[ns]
	return d, ok
}

func (h *Hearsay) Domains() []Domain {
	ds := slices.Collect(maps.Values(h.domains))
	slices.SortFunc(ds, func(a, b Domain) int {
		return cmp.Compare(a.Namespace(), b.Namespace())
	})
	return ds
}

// Observe records ground truth learned as a byproduct of real work.
// For Bool domains the value is 0 or 1.
func (h *Hearsay) Observe(ns string, subject any, value float64, at time.Time) error {
	d, ok := h.domains[ns]
	if !ok {
		return fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	key, err := d.Encode(subject)
	if err != nil {
		return err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("hearsay: observation value must be finite")
	}
	if d.ValueType() == Bool && value != 0 && value != 1 {
		return fmt.Errorf("hearsay: Boolean observation must be 0 or 1")
	}
	if err := h.store.observe(ns, key, value, at); err != nil {
		return err
	}
	h.metrics.recordObservation(ns)
	h.mu.Lock()
	h.dirty[ns]++
	h.mu.Unlock()
	return nil
}

// Report feeds back the outcome of acting on a hint. The domain
// converts the outcome into ground truth; every followed source that
// claimed on the subject is scored against it.
func (h *Hearsay) Report(ns string, subject any, outcome any) error {
	d, ok := h.domains[ns]
	if !ok {
		return fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	truth := d.Verify(subject, outcome)
	if !truth.Known {
		return nil
	}
	key, err := d.Encode(subject)
	if err != nil {
		return err
	}
	now := time.Now()
	policy := d.Freshness()
	outcomes := map[string]bool{}
	agreements := 0
	h.store.mu.RLock()
	for feed, gens := range h.store.feeds[ns] {
		if feed == h.self {
			continue
		}
		latest := gens[len(gens)-1]
		v, observedAt, ok := payloadLookup(latest.Type, latest.Payload, key)
		if !ok || max(now.Sub(observedAt), 0) > policy.MaxAge {
			continue
		}
		agree := math.Abs(v-truth.Value) <= 0.15
		if d.ValueType() == Bool {
			agree = (v >= 0.5) == (truth.Value >= 0.5)
		}
		outcomes[feed] = agree
		if agree {
			agreements++
		}
	}
	h.store.mu.RUnlock()
	err = errors.Join(h.rep.recordMany(outcomes), h.Observe(ns, subject, truth.Value, now))
	h.metrics.recordOutcome(ns, len(outcomes), agreements)
	return err
}

// Publish builds a fresh generation from current local ground truth.
// It returns nil when there is nothing new to publish: no observations
// since the last generation, or no observations at all.
func (h *Hearsay) Publish(ns string) (*Generation, error) {
	d, ok := h.domains[ns]
	if !ok {
		return nil, fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	now := time.Now()
	pruned, err := h.store.pruneTruth(ns, now.Add(-d.Freshness().MaxAge))
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if pruned {
		h.dirty[ns]++
	}
	revision := h.dirty[ns]
	h.mu.Unlock()
	if revision == 0 {
		return nil, nil
	}

	h.store.mu.RLock()
	var payload []byte
	var count int
	if d.ValueType() == Bool {
		payload, count = encodeBoolPayload(h.store.truth[ns], d.Freshness(), now)
	} else {
		payload, count = encodeScalarPayload(h.store.truth[ns], d.Freshness(), now)
	}
	seq := uint64(1)
	if gens := h.store.feeds[ns][h.self]; len(gens) > 0 {
		seq = gens[len(gens)-1].Seq + 1
	}
	h.store.mu.RUnlock()
	if uint64(count) > math.MaxUint32 {
		return nil, fmt.Errorf("hearsay: generation has too many claims")
	}

	g, err := BuildGeneration(h.id, ns, d.ValueType(), seq, now, count, payload)
	if err != nil {
		return nil, err
	}
	if err := h.store.addGeneration(g, h.self); err != nil {
		return nil, err
	}
	h.metrics.recordPublished(ns)
	h.mu.Lock()
	if h.dirty[ns] == revision {
		delete(h.dirty, ns)
	}
	h.mu.Unlock()
	return g, nil
}

// Ingest verifies and retains a generation received from the network.
func (h *Hearsay) Ingest(raw []byte) (generation *Generation, err error) {
	ns := ""
	defer func() { h.metrics.recordIngest(ns, err == nil) }()
	g, err := ParseGeneration(raw)
	if err != nil {
		return nil, err
	}
	d, ok := h.domains[g.Namespace]
	if !ok {
		return nil, fmt.Errorf("hearsay: unknown namespace %q", g.Namespace)
	}
	ns = g.Namespace
	if g.Type != d.ValueType() {
		return nil, fmt.Errorf("hearsay: value type mismatch for %q", g.Namespace)
	}
	if g.BuiltAt.After(time.Now().Add(maxSkew)) {
		return nil, ErrFutureGeneration
	}
	if g.FeedID() == h.self {
		return nil, errors.New("hearsay: refusing to ingest own feed")
	}
	if err := h.store.addGeneration(g, h.self); err != nil {
		return nil, err
	}
	return g, nil
}

// Latest returns the newest retained generation for a feed, or nil.
func (h *Hearsay) Latest(ns, feed string) *Generation {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	gens := h.store.feeds[ns][feed]
	if len(gens) == 0 {
		return nil
	}
	return gens[len(gens)-1]
}

// Feeds lists the feed IDs with retained generations in a namespace.
func (h *Hearsay) Feeds(ns string) []string {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	ids := make([]string, 0, len(h.store.feeds[ns]))
	for id := range h.store.feeds[ns] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Forget drops a feed's retained generations, from memory and from
// disk. Narrowing to an explicit set of publishers uses it to discard
// feeds an earlier run discovered: retained generations answer queries
// whether or not the feed is still followed.
func (h *Hearsay) Forget(ns, feed string) error {
	return h.store.forget(ns, feed)
}

// PruneFeedsBefore drops remote feeds whose newest generation predates cutoff.
func (h *Hearsay) PruneFeedsBefore(ns string, cutoff time.Time) ([]string, error) {
	if _, ok := h.domains[ns]; !ok {
		return nil, fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	removed, err := h.store.pruneFeedsBefore(ns, h.self, cutoff)
	if err == nil {
		h.metrics.recordFeedsPruned(ns, len(removed))
	}
	return removed, err
}

// Metrics returns a consistent snapshot of local engine activity.
func (h *Hearsay) Metrics() EngineMetrics {
	return h.metrics.snapshot()
}

func (h *Hearsay) Close() error {
	return errors.Join(h.store.close(), h.metrics.close())
}
