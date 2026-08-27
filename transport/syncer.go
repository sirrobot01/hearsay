package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sirrobot01/hearsay"
)

// Syncer runs the publish and subscribe loop: republish pointers
// (BEP 44 items expire in about two hours), discover participants,
// fetch newer generations from every known feed, and seed what we
// publish and what we fetch.
type Syncer struct {
	Engine   *hearsay.Hearsay
	Node     *Node
	Publish  bool
	Interval time.Duration
	Log      *slog.Logger
}

func (s *Syncer) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = 30 * time.Minute
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	self := hex.EncodeToString(s.Engine.Identity())
	for ns := range s.namespaces() {
		s.Node.Track(ns)
		// A publisher advertises its own feed, or gossip has nothing
		// to spread.
		if s.Publish {
			s.Node.Announce(ns, self)
		}
	}
	for {
		s.sync(ctx)
		select {
		case <-time.After(s.Interval):
		case <-ctx.Done():
			return
		}
	}
}

func (s *Syncer) namespaces() map[string]hearsay.Domain {
	m := map[string]hearsay.Domain{}
	for _, d := range s.Engine.Domains() {
		m[d.Namespace()] = d
	}
	return m
}

// pullCap bounds generation fetches in flight across all namespaces.
// Namespaces themselves sync concurrently: a feed that is slow to
// locate must not hold up every other feed behind it.
const pullCap = 8

const staleFeedGrace = 7 * 24 * time.Hour

func (s *Syncer) sync(ctx context.Context) {
	self := hex.EncodeToString(s.Engine.Identity())
	sem := make(chan struct{}, pullCap)
	var wg sync.WaitGroup
	for ns := range s.namespaces() {
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() { s.syncNamespace(ctx, ns, self, sem) })
	}
	wg.Wait()
}

func (s *Syncer) syncNamespace(ctx context.Context, ns, self string, sem chan struct{}) {
	if s.Publish {
		s.publish(ctx, ns, self)
	}
	domain, ok := s.Engine.Domain(ns)
	if !ok {
		return
	}
	cutoff := time.Now().Add(-domain.Freshness().MaxAge - staleFeedGrace)
	removed, err := s.Engine.PruneFeedsBefore(ns, cutoff)
	if err != nil {
		s.Log.Warn("feed prune failed", "ns", ns, "err", err)
	} else if len(removed) > 0 {
		s.Log.Info("pruned stale feeds", "ns", ns, "count", len(removed))
	}
	s.Node.PruneFeeds(ns, cutoff)
	for _, feed := range s.Engine.Feeds(ns) {
		if feed == self {
			continue
		}
		if latest := s.Engine.Latest(ns, feed); latest != nil {
			s.Node.AddRetainedFeed(ns, feed, latest.BuiltAt)
		}
	}
	// Discovery runs even with no known feeds: it is the only way to
	// learn the first publisher in a namespace.
	s.Node.Discover(ctx, ns)
	var wg sync.WaitGroup
	for _, feed := range s.Node.ReadyFeeds(ns) {
		if feed == self {
			continue
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			s.Node.RecordPull(ns, feed, s.pull(ctx, ns, feed, cutoff))
		})
	}
	wg.Wait()
}

func (s *Syncer) publish(ctx context.Context, ns, self string) {
	if g, err := s.Engine.Publish(ns); err != nil {
		s.Log.Warn("publish failed", "ns", ns, "err", err)
	} else if g != nil {
		s.Log.Info("published generation", "ns", ns, "seq", g.Seq, "count", g.Count)
	}
	g := s.Engine.Latest(ns, self)
	if g == nil {
		return
	}
	raw, err := g.MarshalBinary()
	if err != nil {
		return
	}
	ih, err := s.Node.Seed(raw)
	if err != nil {
		s.Log.Warn("seed failed", "ns", ns, "err", err)
		return
	}
	ptr := Pointer{Gen: g.Seq, InfoHash: ih.HexString(), Size: int64(len(raw)), BuiltAt: g.BuiltAt.Unix()}
	putCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.Node.PutPointer(putCtx, s.Engine.PrivateIdentity(), ns, ptr); err != nil {
		s.Log.Warn("pointer publish failed", "ns", ns, "err", err)
	}
}

// fetchTimeout budgets one attempt at a generation blob. A publisher
// behind CGNAT cannot be dialled: it finds leechers by watching the
// DHT for the infohash and dialling out. Fetching therefore costs an
// announce plus the publisher's next scrape, not a round trip, and a
// budget shorter than that fails every first attempt on a fresh
// pointer while the retry a cycle later succeeds. Abandoning the fetch
// also withdraws the announce the publisher is waiting to see, so the
// budget must cover several of its scrape cycles.
const fetchTimeout = 15 * time.Minute

func (s *Syncer) pull(ctx context.Context, ns, feed string, cutoff time.Time) bool {
	pub, err := hex.DecodeString(feed)
	if err != nil || len(pub) != 32 {
		return false
	}
	getCtx, cancel := context.WithTimeout(ctx, time.Minute)
	ptr, err := s.Node.GetPointer(getCtx, pub, ns)
	cancel()
	if err != nil {
		return false
	}
	if time.Unix(ptr.BuiltAt, 0).Before(cutoff) {
		return false
	}
	if latest := s.Engine.Latest(ns, feed); latest != nil && ptr.Gen <= latest.Seq {
		// Already current: keep seeding the retained copy so this node
		// stays a relay for unconnectable publishers across restarts.
		if raw, err := latest.MarshalBinary(); err == nil {
			s.Node.Seed(raw)
		}
		return true
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	raw, err := s.Node.Fetch(fetchCtx, ptr.InfoHash)
	cancel()
	if err != nil {
		s.Log.Warn("fetch failed", "ns", ns, "feed", feed[:8], "err", err)
		return false
	}
	parsed, err := hearsay.ParseGeneration(raw)
	if err != nil || parsed.FeedID() != feed || parsed.Namespace != ns {
		s.Log.Warn("generation does not match pointer", "ns", ns, "feed", feed[:8])
		return false
	}
	g, err := s.Engine.Ingest(raw)
	if err != nil {
		if !errors.Is(err, hearsay.ErrStale) {
			s.Log.Warn("ingest rejected", "ns", ns, "feed", feed[:8], "err", err)
			return false
		}
		return s.Engine.Latest(ns, feed) != nil
	}
	s.Log.Info("ingested generation", "ns", ns, "feed", feed[:8], "seq", g.Seq, "count", g.Count)
	return true
}
