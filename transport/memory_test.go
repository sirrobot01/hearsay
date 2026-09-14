package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// memoryTestNode disables discovery and peer connections for local tests.
func memoryTestNode(t testing.TB) *Node {
	t.Helper()
	dir := t.TempDir()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DialForPeerConns = false
	cfg.AcceptPeerConnections = false
	client, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	gossip, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gossip.Close() })
	return &Node{
		dir: dir, client: client, gossip: gossip,
		maxStorage: DefaultMaxStorage, maxSeeded: DefaultMaxSeededTorrents,
		known:  map[string]map[string]*feedState{},
		seeded: map[*torrent.Torrent]time.Time{}, fetching: map[*torrent.Torrent]int{},
	}
}

func TestSeededTorrentsBoundedWithoutDeletingBlobs(t *testing.T) {
	n := memoryTestNode(t)
	var seeded []*torrent.Torrent
	for i := range 4 {
		ih, err := n.Seed([]byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		tr, ok := n.client.Torrent(ih)
		if !ok {
			t.Fatal("seeded torrent not registered")
		}
		if err := tr.VerifyDataContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		seeded = append(seeded, tr)
		n.seeded[tr] = time.Now().Add(time.Duration(i-4) * time.Hour)
	}
	n.maxSeeded = 2
	n.fetching[seeded[0]] = 1
	n.pruneSeededLocked(seeded[3])
	if len(n.client.Torrents()) != 2 {
		t.Fatalf("retained %d torrents, want 2", len(n.client.Torrents()))
	}
	for _, i := range []int{0, 3} {
		select {
		case <-seeded[i].Closed():
			t.Fatalf("dropped protected torrent %d", i)
		default:
		}
	}
	for _, tr := range seeded {
		if _, err := os.Stat(filepath.Join(n.dir, tr.Info().Name)); err != nil {
			t.Fatalf("removed reusable blob: %v", err)
		}
	}
	delete(n.fetching, seeded[0])
	ih, err := n.Seed([]byte{4})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-seeded[0].Closed():
	default:
		t.Fatal("old idle torrent was not dropped")
	}
	if len(n.client.Torrents()) != 2 {
		t.Fatalf("retained %d torrents after seeding, want 2", len(n.client.Torrents()))
	}
	// A retained generation must still support local reads.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, err := n.Fetch(ctx, ih.HexString())
	if err != nil || !bytes.Equal(raw, []byte{4}) {
		t.Fatalf("fetch retained blob = %v, %v", raw, err)
	}
	// An evicted generation can be seeded again from its existing file.
	if _, err := n.Seed([]byte{0}); err != nil {
		t.Fatalf("seed evicted blob: %v", err)
	}
}

func TestSeedPruningWaitsForFetch(t *testing.T) {
	n := memoryTestNode(t)
	for i := range 2 {
		ih, err := n.Seed([]byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := n.client.Torrent(ih)
		if err := tr.VerifyDataContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		n.fetching[tr] = 1
	}
	n.maxSeeded = 1
	n.pruneSeededLocked(nil)
	if len(n.seeded) != 2 {
		t.Fatal("dropped a torrent with an active fetch")
	}
	clear(n.fetching)
	n.pruneSeededLocked(nil)
	if len(n.seeded) != 1 {
		t.Fatal("did not prune after fetches finished")
	}
}

func TestCancelledFetchReleasesTorrent(t *testing.T) {
	n := memoryTestNode(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := n.Fetch(ctx, (metainfo.Hash{1}).HexString()); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v", err)
	}
	if len(n.client.Torrents()) != 0 || len(n.fetching) != 0 {
		t.Fatal("cancelled fetch retained its torrent")
	}
}

func TestCancelledFetchKeepsSharedTorrent(t *testing.T) {
	n := memoryTestNode(t)
	ih := metainfo.Hash{1}
	tr, _ := n.client.AddTorrentInfoHash(ih)
	n.fetching[tr] = 1
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := n.Fetch(ctx, ih.HexString()); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v", err)
	}
	select {
	case <-tr.Closed():
		t.Fatal("cancelled fetch dropped another reader's torrent")
	default:
	}
	if n.fetching[tr] != 1 {
		t.Fatalf("remaining fetch count = %d, want 1", n.fetching[tr])
	}
}

func TestCloseReleasesConcurrentFetches(t *testing.T) {
	n := memoryTestNode(t)
	ih := (metainfo.Hash{1}).HexString()
	errs := make(chan error, 8)
	for range cap(errs) {
		go func() {
			_, err := n.Fetch(t.Context(), ih)
			errs <- err
		}()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		n.storageMu.Lock()
		var count int
		for _, readers := range n.fetching {
			count += readers
		}
		n.storageMu.Unlock()
		if count == cap(errs) {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("fetches did not share the torrent")
		}
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	for range cap(errs) {
		select {
		case err := <-errs:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("fetch after shutdown = %v", err)
			}
		case <-deadline.C:
			t.Fatal("shutdown left a fetch blocked")
		}
	}
	if len(n.fetching) != 0 || len(n.seeded) != 0 || len(n.client.Torrents()) != 0 {
		t.Fatal("shutdown retained torrent state")
	}
	if _, err := n.Fetch(t.Context(), ih); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("new fetch after shutdown = %v", err)
	}
	if _, err := n.Seed([]byte("closed")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("new seed after shutdown = %v", err)
	}
}

func TestStoragePruningPreservesFetchAndClearsSeedState(t *testing.T) {
	n := memoryTestNode(t)
	var seeded []*torrent.Torrent
	for i := range 2 {
		ih, err := n.Seed([]byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := n.client.Torrent(ih)
		if err := tr.VerifyDataContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		seeded = append(seeded, tr)
	}
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	n.maxStorage = 1
	n.fetching[seeded[0]] = 1
	protected := seeded[1].Info().Name
	if err := n.pruneStorageLocked(protected); err != nil {
		t.Fatal(err)
	}
	if len(n.seeded) != 2 || len(n.client.Torrents()) != 2 {
		t.Fatal("storage pruning closed an active fetch")
	}
	delete(n.fetching, seeded[0])
	if err := n.pruneStorageLocked(protected); err != nil {
		t.Fatal(err)
	}
	if len(n.seeded) != 1 || len(n.client.Torrents()) != 1 {
		t.Fatal("storage pruning retained the removed torrent")
	}
	if _, err := os.Stat(filepath.Join(n.dir, seeded[0].Info().Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old blob was not removed: %v", err)
	}
}

func TestRejectNegativeSeededTorrentLimit(t *testing.T) {
	if _, err := ListenWithConfig(NodeConfig{Dir: t.TempDir(), MaxSeededTorrents: -1}); err == nil {
		t.Fatal("negative seeded torrent limit accepted")
	}
}

func TestStatsAllocationDoesNotScaleWithPendingPeers(t *testing.T) {
	n := memoryTestNode(t)
	tr, _ := n.client.AddTorrentInfoHash(metainfo.Hash{1})
	peers := make([]torrent.PeerInfo, 500)
	for i := range peers {
		peers[i] = torrent.PeerInfo{Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: i + 1000}}
	}
	tr.AddPeers(peers)
	if got := tr.Stats().PendingPeers; got != len(peers) {
		t.Fatalf("pending peers = %d, want %d", got, len(peers))
	}
	allocs := testing.AllocsPerRun(10, func() { n.Stats() })
	if allocs > 100 {
		t.Fatalf("statistics allocated %.0f objects for idle peers", allocs)
	}
	if got := n.Stats().Torrents[0].ActivePeers; got != 0 {
		t.Fatalf("active peers = %d, want 0", got)
	}
}

func BenchmarkActivePeerCount(b *testing.B) {
	n := memoryTestNode(b)
	tr, _ := n.client.AddTorrentInfoHash(metainfo.Hash{1})
	peers := make([]torrent.PeerInfo, 500)
	for i := range peers {
		peers[i] = torrent.PeerInfo{Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: i + 1000}}
	}
	tr.AddPeers(peers)
	b.Run("full_stats", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = tr.Stats().ActivePeers
		}
	})
	b.Run("active_connections", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = len(tr.PeerConns())
		}
	})
}
