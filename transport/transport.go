// Package transport moves Hearsay generations between operators with
// no infrastructure: BEP 44 mutable items for follow-by-pubkey,
// synthetic DHT topics for discovery, and torrent swarms for payload
// distribution. A publisher behind CGNAT dials outward to leechers;
// connectability is lost, participation is not.
package transport

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
)

// MaxGenerationSize bounds fetched blobs: a bloom filter for a
// million subjects is about 1.2MB, so this is generous.
const MaxGenerationSize = 64 << 20

// DefaultMaxStorage bounds generation blobs retained for relay.
const DefaultMaxStorage = 1 << 30

// DefaultMaxSeededTorrents limits retained swarms. Active transfers can exceed it.
const DefaultMaxSeededTorrents = 256

// DefaultMaxFeedsPerNamespace bounds publisher metadata retained per namespace.
const DefaultMaxFeedsPerNamespace = 256

const (
	dialCap              = 8
	discoveryMaxPeers    = 64
	feedFailureLimit     = 3
	feedRetryBase        = 5 * time.Minute
	feedRetryMax         = 6 * time.Hour
	feedQuarantinePeriod = 24 * time.Hour
)

type feedState struct {
	lastSeen    time.Time
	retryAt     time.Time
	failures    uint8
	pinned      bool
	quarantined bool
}

type Node struct {
	dir        string
	client     *torrent.Client
	dhts       []*dht.Server
	gossip     net.Listener
	maxStorage int64
	maxFeeds   int
	maxSeeded  int
	blocked    peerBlocklist

	mu        sync.Mutex
	storageMu sync.Mutex
	self      string
	known     map[string]map[string]*feedState
	allow     map[string]struct{}            // nil = accept any discovered key
	seeded    map[*torrent.Torrent]time.Time // guarded by storageMu
	fetching  map[*torrent.Torrent]int       // guarded by storageMu
}

type NodeConfig struct {
	Dir                  string
	Port                 int
	GossipPort           int
	MaxStorage           int64
	MaxFeedsPerNamespace int
	// MaxSeededTorrents bounds retained swarms. Zero selects the default.
	// Active transfers can exceed this limit until they become idle.
	MaxSeededTorrents int
	// BlockedPeers lists address ranges the node never connects to: swarm
	// peers, DHT nodes, and gossip partners. PrivatePeerPrefixes holds the
	// ranges that are unreachable across the public internet.
	BlockedPeers []netip.Prefix
}

// PrivatePeerPrefixes are the address ranges that a public peer can never
// have: RFC 1918, CGNAT, link-local, loopback, and IPv6 unique local. Behind
// a VPN, DHT returns such addresses from the provider's internal network.
var PrivatePeerPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

// ParseBlockedPeers returns the prefixes for NodeConfig.BlockedPeers:
// PrivatePeerPrefixes when private is set, followed by each CIDR in cidrs.
func ParseBlockedPeers(private bool, cidrs []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	if private {
		prefixes = slices.Clone(PrivatePeerPrefixes)
	}
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("transport: blocked peer range %q: %w", cidr, err)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

// peerBlocklist implements iplist.Ranger over CIDR prefixes. A linear scan
// is fine for a handful of prefixes and, unlike iplist.IPList, tolerates
// overlapping ranges.
type peerBlocklist []netip.Prefix

func (b peerBlocklist) blocks(ip net.IP) bool {
	_, ok := b.Lookup(ip)
	return ok
}

func (b peerBlocklist) Lookup(ip net.IP) (iplist.Range, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return iplist.Range{}, false
	}
	addr = addr.Unmap()
	for _, p := range b {
		if !p.Contains(addr) {
			continue
		}
		p = p.Masked()
		last := iplist.IPNetLast(&net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())})
		return iplist.Range{First: p.Addr().AsSlice(), Last: last, Description: "blocked peer range " + p.String()}, true
	}
	return iplist.Range{}, false
}

func (b peerBlocklist) NumRanges() int { return len(b) }

// Listen starts the swarm client, its DHT servers, and the gossip
// listener. dir stores generation blobs for seeding. Ports of 0 pick
// random ports; connectable nodes pin them so a firewall can open
// them.
func Listen(dir string, port, gossipPort int) (*Node, error) {
	return ListenWithConfig(NodeConfig{Dir: dir, Port: port, GossipPort: gossipPort})
}

func ListenWithConfig(config NodeConfig) (*Node, error) {
	if config.MaxStorage < 0 {
		return nil, fmt.Errorf("transport: max storage must not be negative")
	}
	if config.MaxFeedsPerNamespace < 0 {
		return nil, fmt.Errorf("transport: max feeds must not be negative")
	}
	if config.MaxSeededTorrents < 0 {
		return nil, fmt.Errorf("transport: max seeded torrents must not be negative")
	}
	if err := os.MkdirAll(config.Dir, 0o755); err != nil {
		return nil, err
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = config.Dir
	cfg.Seed = true
	cfg.ListenPort = config.Port
	blocked := peerBlocklist(config.BlockedPeers)
	if len(blocked) > 0 {
		// The client hands this list to its DHT servers as well.
		cfg.IPBlocklist = blocked
	}
	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	n := &Node{
		dir:        config.Dir,
		client:     client,
		maxStorage: config.MaxStorage,
		maxFeeds:   config.MaxFeedsPerNamespace,
		maxSeeded:  config.MaxSeededTorrents,
		blocked:    blocked,
		known:      map[string]map[string]*feedState{},
		seeded:     map[*torrent.Torrent]time.Time{},
		fetching:   map[*torrent.Torrent]int{},
	}
	if n.maxStorage == 0 {
		n.maxStorage = DefaultMaxStorage
	}
	if n.maxFeeds == 0 {
		n.maxFeeds = DefaultMaxFeedsPerNamespace
	}
	if n.maxSeeded == 0 {
		n.maxSeeded = DefaultMaxSeededTorrents
	}
	for _, s := range client.DhtServers() {
		if w, ok := s.(torrent.AnacrolixDhtServerWrapper); ok {
			n.dhts = append(n.dhts, w.Server)
		}
	}
	n.gossip, err = net.Listen("tcp", fmt.Sprintf(":%d", config.GossipPort))
	if err != nil {
		client.Close()
		return nil, err
	}
	if err := n.pruneStorage(""); err != nil {
		n.Close()
		return nil, err
	}
	go n.serveGossip()
	go func() {
		tick := time.Tick(time.Minute)
		for {
			select {
			case <-client.Closed():
				return
			case <-tick:
				n.storageMu.Lock()
				n.pruneSeededLocked(nil)
				n.storageMu.Unlock()
			}
		}
	}()
	return n, nil
}

func (n *Node) Close() error {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	select {
	case <-n.client.Closed():
		return nil
	default:
	}
	n.gossip.Close()
	n.client.Close()
	clear(n.seeded)
	return nil
}

// Track opens a namespace for gossip: feeds offered for untracked
// namespaces are ignored.
func (n *Node) Track(ns string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.known == nil {
		n.known = map[string]map[string]*feedState{}
	}
	if n.known[ns] == nil {
		n.known[ns] = map[string]*feedState{}
	}
}

// Announce registers this node's own publisher key for a namespace so
// gossip has something to spread, and records it as self so peer
// counts can leave it out.
func (n *Node) Announce(ns, self string) bool {
	n.mu.Lock()
	n.self = self
	n.mu.Unlock()
	return n.PinFeed(ns, self)
}

// Allow restricts the node to a fixed set of publisher keys. Discovery
// and gossip can then contribute nothing: AddFeed rejects every other
// key and Discover stops announcing. Without a call, any discovered key
// is a candidate. Pass the node's own key too, or it cannot advertise
// its own feed.
func (n *Node) Allow(pubHex ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.allow = make(map[string]struct{}, len(pubHex))
	for _, k := range pubHex {
		n.allow[k] = struct{}{}
	}
	for _, feeds := range n.known {
		maps.DeleteFunc(feeds, func(id string, _ *feedState) bool {
			_, ok := n.allow[id]
			return !ok
		})
	}
}

// AddFeed registers a publisher key for a namespace, from config or
// from discovery. Keys are candidates only: their pointers and
// generations still verify cryptographically, and their weight is
// earned through outcomes. An allowlist rejects everything outside it.
func (n *Node) AddFeed(ns, pubHex string) bool {
	now := time.Now()
	return n.addFeedAt(ns, pubHex, false, now, now)
}

// AddRetainedFeed restores a candidate from a retained generation without
// making an old generation look newly discovered.
func (n *Node) AddRetainedFeed(ns, pubHex string, builtAt time.Time) bool {
	return n.addFeedAt(ns, pubHex, false, builtAt, time.Now())
}

// PinFeed protects an explicitly followed or local feed from eviction.
func (n *Node) PinFeed(ns, pubHex string) bool {
	now := time.Now()
	return n.addFeedAt(ns, pubHex, true, now, now)
}

func (n *Node) addFeedAt(ns, pubHex string, pinned bool, seenAt, now time.Time) bool {
	if raw, err := hex.DecodeString(pubHex); err != nil || len(raw) != 32 {
		return false
	}
	if seenAt.After(now) {
		seenAt = now
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.known == nil {
		n.known = map[string]map[string]*feedState{}
	}
	if n.allow != nil {
		if _, ok := n.allow[pubHex]; !ok {
			return false
		}
	}
	if n.known[ns] == nil {
		n.known[ns] = map[string]*feedState{}
	}
	if state := n.known[ns][pubHex]; state != nil {
		state.pinned = state.pinned || pinned
		if state.quarantined {
			if now.Before(state.retryAt) && !pinned {
				return false
			}
			state.quarantined = false
			state.failures = 0
			state.retryAt = time.Time{}
		}
		if seenAt.After(state.lastSeen) {
			state.lastSeen = seenAt
		}
		return false
	}
	if len(n.known[ns]) >= n.maxFeedsLocked() {
		candidate := n.evictableFeedLocked(ns)
		if candidate == "" {
			return false
		}
		delete(n.known[ns], candidate)
	}
	n.known[ns][pubHex] = &feedState{lastSeen: seenAt, pinned: pinned}
	return true
}

// Feeds lists known publisher keys for a namespace.
func (n *Node) Feeds(ns string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := slices.Collect(maps.Keys(n.known[ns]))
	ids = slices.DeleteFunc(ids, func(id string) bool { return n.known[ns][id].quarantined })
	slices.Sort(ids)
	return ids
}

// ReadyFeeds lists candidates whose retry delay has elapsed.
func (n *Node) ReadyFeeds(ns string) []string {
	return n.readyFeedsAt(ns, time.Now())
}

func (n *Node) readyFeedsAt(ns string, now time.Time) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := slices.Collect(maps.Keys(n.known[ns]))
	ids = slices.DeleteFunc(ids, func(id string) bool {
		state := n.known[ns][id]
		return state.quarantined || now.Before(state.retryAt)
	})
	slices.Sort(ids)
	return ids
}

// RecordPull applies retry backoff and quarantines repeatedly failing feeds.
func (n *Node) RecordPull(ns, feed string, success bool) {
	n.recordPullAt(ns, feed, success, time.Now())
}

func (n *Node) recordPullAt(ns, feed string, success bool, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	state := n.known[ns][feed]
	if state == nil {
		return
	}
	if success {
		state.failures = 0
		state.retryAt = time.Time{}
		state.quarantined = false
		state.lastSeen = now
		return
	}
	if state.failures < 31 {
		state.failures++
	}
	if state.failures >= feedFailureLimit && !state.pinned {
		state.quarantined = true
		state.retryAt = now.Add(feedQuarantinePeriod)
		return
	}
	shift := min(state.failures-1, uint8(6))
	delay := feedRetryBase << shift
	state.retryAt = now.Add(min(delay, feedRetryMax))
}

// PruneFeeds removes expired candidate metadata and completed quarantines.
func (n *Node) PruneFeeds(ns string, before time.Time) {
	n.pruneFeedsAt(ns, before, time.Now())
}

func (n *Node) pruneFeedsAt(ns string, before, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, state := range n.known[ns] {
		if state.pinned {
			continue
		}
		if state.lastSeen.Before(before) || state.quarantined && !now.Before(state.retryAt) {
			delete(n.known[ns], id)
		}
	}
}

func (n *Node) maxFeedsLocked() int {
	if n.maxFeeds > 0 {
		return n.maxFeeds
	}
	return DefaultMaxFeedsPerNamespace
}

func (n *Node) evictableFeedLocked(ns string) string {
	var candidate string
	for id, state := range n.known[ns] {
		if state.pinned || id == n.self {
			continue
		}
		if candidate == "" {
			candidate = id
			continue
		}
		current := n.known[ns][candidate]
		if state.quarantined != current.quarantined {
			if state.quarantined {
				candidate = id
			}
			continue
		}
		if state.failures > current.failures || state.failures == current.failures && state.lastSeen.Before(current.lastSeen) {
			candidate = id
		}
	}
	return candidate
}

// TorrentStat describes one generation blob this node serves or is
// fetching, and how many peers it is exchanging data with right now.
type TorrentStat struct {
	InfoHash    string `json:"infohash"`
	Size        int64  `json:"size"`
	Completed   int64  `json:"completed"`
	ActivePeers int    `json:"active_peers"`
}

// Stats is a point-in-time view of network participation.
type Stats struct {
	SwarmPort            int            `json:"swarm_port"`
	GossipPort           int            `json:"gossip_port"`
	StoredBytes          int64          `json:"stored_bytes"`
	MaxStorage           int64          `json:"max_storage_bytes"`
	MaxFeedsPerNamespace int            `json:"max_feeds_per_namespace"`
	MaxSeededTorrents    int            `json:"max_seeded_torrents"`
	Torrents             []TorrentStat  `json:"torrents"`
	Feeds                map[string]int `json:"feeds"`
	RetryingFeeds        map[string]int `json:"retrying_feeds"`
	QuarantinedFeeds     map[string]int `json:"quarantined_feeds"`
}

func (n *Node) Stats() Stats {
	s := Stats{
		SwarmPort:            n.client.LocalPort(),
		MaxStorage:           n.maxStorage,
		MaxFeedsPerNamespace: n.maxFeeds,
		MaxSeededTorrents:    n.maxSeeded,
		Feeds:                map[string]int{},
		RetryingFeeds:        map[string]int{},
		QuarantinedFeeds:     map[string]int{},
	}
	if a, ok := n.gossip.Addr().(*net.TCPAddr); ok {
		s.GossipPort = a.Port
	}
	torrents := n.client.Torrents()
	s.Torrents = make([]TorrentStat, 0, len(torrents))
	for _, t := range torrents {
		st := TorrentStat{
			InfoHash:    t.InfoHash().HexString(),
			ActivePeers: len(t.PeerConns()),
		}
		if t.Info() != nil {
			st.Size = t.Length()
			st.Completed = t.BytesCompleted()
		}
		s.Torrents = append(s.Torrents, st)
	}
	s.StoredBytes = n.storageUsage()
	n.mu.Lock()
	now := time.Now()
	for ns, feeds := range n.known {
		s.Feeds[ns] = n.peersLocked(ns)
		for _, state := range feeds {
			if state.quarantined {
				s.QuarantinedFeeds[ns]++
			} else if now.Before(state.retryAt) {
				s.RetryingFeeds[ns]++
			}
		}
	}
	n.mu.Unlock()
	return s
}

// peersLocked counts the publisher keys known for a namespace without
// this node's own. Announce registers self so gossip has something to
// spread, but sync never pulls from it, so reporting it as a peer
// overstates the network by one in every namespace. Caller holds n.mu.
func (n *Node) peersLocked(ns string) int {
	c := 0
	for id, state := range n.known[ns] {
		if id != n.self && !state.quarantined {
			c++
		}
	}
	return c
}

// Seed writes a generation blob into the swarm directory and serves
// it as a single-file torrent.
func (n *Node) Seed(blob []byte) (metainfo.Hash, error) {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	select {
	case <-n.client.Closed():
		return metainfo.Hash{}, net.ErrClosed
	default:
	}

	sum := sha256.Sum256(blob)
	name := hex.EncodeToString(sum[:]) + ".gen"
	path := filepath.Join(n.dir, name)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			return metainfo.Hash{}, err
		}
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return metainfo.Hash{}, err
	}
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		return metainfo.Hash{}, err
	}
	mi := &metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info)}
	t, err := n.client.AddTorrent(mi)
	if err != nil {
		return metainfo.Hash{}, err
	}
	<-t.GotInfo()
	t.DownloadAll()
	n.seeded[t] = time.Now()
	n.pruneSeededLocked(t)
	if err := n.pruneStorageLocked(name); err != nil {
		return metainfo.Hash{}, err
	}
	return t.InfoHash(), nil
}

// Fetch downloads a generation blob from its swarm. Fetched blobs
// keep seeding afterward, so popular publishers do not bear the load.
func (n *Node) Fetch(ctx context.Context, infohash string) ([]byte, error) {
	var ih metainfo.Hash
	if err := ih.FromHexString(infohash); err != nil {
		return nil, err
	}
	n.storageMu.Lock()
	select {
	case <-n.client.Closed():
		n.storageMu.Unlock()
		return nil, net.ErrClosed
	default:
	}
	t, _ := n.client.AddTorrentInfoHash(ih)
	n.fetching[t]++
	n.storageMu.Unlock()
	defer func() {
		n.storageMu.Lock()
		n.fetching[t]--
		if n.fetching[t] == 0 {
			delete(n.fetching, t)
			if _, retained := n.seeded[t]; !retained {
				t.Drop()
			}
		}
		n.pruneSeededLocked(nil)
		n.storageMu.Unlock()
	}()
	select {
	case <-t.GotInfo():
	case <-t.Closed():
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if t.Length() > MaxGenerationSize || t.Info().IsDir() {
		return nil, fmt.Errorf("transport: not a generation blob: %d bytes", t.Length())
	}
	t.DownloadAll()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for t.BytesCompleted() < t.Length() {
		select {
		case <-ticker.C:
		case <-t.Closed():
			return nil, net.ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// The reader avoids trusting the info dict's file name with a
	// filesystem path.
	r := t.NewReader()
	raw, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil, err
	}
	name := t.Info().Name
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".gen") {
		name = ""
	}
	n.storageMu.Lock()
	select {
	case <-t.Closed():
		n.storageMu.Unlock()
		return nil, net.ErrClosed
	default:
	}
	if name != "" {
		now := time.Now()
		if err := os.Chtimes(filepath.Join(n.dir, name), now, now); err != nil {
			n.storageMu.Unlock()
			return nil, err
		}
	}
	err = n.pruneStorageLocked(name)
	n.seeded[t] = time.Now()
	n.storageMu.Unlock()
	return raw, err
}

// pruneSeededLocked drops old, idle swarms but keeps their files for reuse.
// Callers must hold storageMu. A fetch keeps its swarm until its reader closes.
func (n *Node) pruneSeededLocked(protected *torrent.Torrent) {
	for t := range n.seeded {
		select {
		case <-t.Closed():
			delete(n.seeded, t)
		default:
		}
	}
	limit := n.maxSeeded
	if limit == 0 {
		limit = DefaultMaxSeededTorrents
	}
	if len(n.seeded) <= limit {
		return
	}
	idle := make([]*torrent.Torrent, 0, len(n.seeded))
	for t := range n.seeded {
		if t != protected && n.fetching[t] == 0 && t.Info() != nil &&
			t.BytesCompleted() == t.Length() && len(t.PeerConns()) == 0 {
			idle = append(idle, t)
		}
	}
	slices.SortFunc(idle, func(a, b *torrent.Torrent) int {
		return n.seeded[a].Compare(n.seeded[b])
	})
	for _, t := range idle {
		if len(n.seeded) <= limit {
			break
		}
		t.Drop()
		delete(n.seeded, t)
	}
}

type storageEntry struct {
	name     string
	size     int64
	modified time.Time
}

func (n *Node) storageUsage() int64 {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	entries, _ := os.ReadDir(n.dir)
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".gen") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

func (n *Node) pruneStorage(protected string) error {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()
	return n.pruneStorageLocked(protected)
}

func (n *Node) pruneStorageLocked(protected string) error {
	entries, err := os.ReadDir(n.dir)
	if err != nil {
		return err
	}
	files := make([]storageEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".gen") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, storageEntry{entry.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= n.maxStorage {
		return nil
	}
	slices.SortFunc(files, func(a, b storageEntry) int {
		if c := a.modified.Compare(b.modified); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	for _, file := range files {
		if total <= n.maxStorage {
			break
		}
		if file.name == protected {
			continue
		}
		inUse := false
		if n.client != nil {
			for _, t := range n.client.Torrents() {
				if info := t.Info(); info != nil && info.Name == file.name {
					if n.fetching[t] > 0 || len(t.PeerConns()) > 0 || t.BytesCompleted() < t.Length() {
						inUse = true
						break
					}
					t.Drop()
					delete(n.seeded, t)
					break
				}
			}
		}
		if inUse {
			continue
		}
		if err := os.Remove(filepath.Join(n.dir, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= file.size
	}
	return nil
}

// Discover announces the gossip port on the namespace topic, walks
// responding participants, and exchanges known publisher keys with a
// bounded number of them. Junk announcers cost connection attempts
// only. An allowlist makes discovery a no-op: it could add nothing, and
// announcing would publish this node's address for no gain.
func (n *Node) Discover(ctx context.Context, ns string) {
	n.mu.Lock()
	restricted := n.allow != nil
	n.mu.Unlock()
	if restricted {
		return
	}
	topic := Topic(ns)
	port := n.gossip.Addr().(*net.TCPAddr).Port
	seen := map[string]struct{}{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, dialCap)
	for _, srv := range n.dhts {
		a, err := srv.AnnounceTraversal(topic, dht.AnnouncePeer(dht.AnnouncePeerOpts{Port: port}))
		if err != nil {
			continue
		}
	drain:
		for {
			select {
			case pv, ok := <-a.Peers:
				if !ok {
					break drain
				}
				for _, peer := range pv.Peers {
					if n.blocked.blocks(peer.IP) {
						continue
					}
					addr := net.JoinHostPort(peer.IP.String(), strconv.Itoa(peer.Port))
					if _, dup := seen[addr]; dup || peer.Port == 0 || len(seen) >= discoveryMaxPeers {
						continue
					}
					seen[addr] = struct{}{}
					wg.Go(func() {
						sem <- struct{}{}
						defer func() { <-sem }()
						n.exchange(ctx, addr)
					})
				}
			case <-ctx.Done():
				break drain
			}
		}
		a.Close()
	}
	wg.Wait()
}

type gossipMsg struct {
	V     int                 `json:"v"`
	Feeds map[string][]string `json:"feeds"`
}

const (
	gossipVersion       = 2
	gossipMaxNamespaces = 16
	gossipMaxFeeds      = 64
	gossipMaxBytes      = 1 << 20
	gossipTimeout       = 15 * time.Second
)

func (n *Node) serveGossip() {
	for {
		conn, err := n.gossip.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(gossipTimeout))
			n.talk(conn)
		}()
	}
}

func (n *Node) exchange(ctx context.Context, addr string) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(gossipTimeout))
	n.talk(conn)
}

// talk is the whole gossip protocol: each side sends one JSON message
// listing the publisher keys it knows per namespace, and merges what
// it receives. JSON objects are self-delimiting, so both sides can
// write first.
func (n *Node) talk(conn net.Conn) {
	n.mu.Lock()
	msg := gossipMsg{V: gossipVersion, Feeds: make(map[string][]string, len(n.known))}
	namespaces := slices.Sorted(maps.Keys(n.known))
	for _, ns := range namespaces[:min(len(namespaces), gossipMaxNamespaces)] {
		feeds := n.known[ns]
		ids := slices.Collect(maps.Keys(feeds))
		ids = slices.DeleteFunc(ids, func(id string) bool { return feeds[id].quarantined })
		slices.Sort(ids)
		msg.Feeds[ns] = ids[:min(len(ids), gossipMaxFeeds)]
	}
	n.mu.Unlock()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		json.NewEncoder(conn).Encode(msg)
	}()
	var theirs gossipMsg
	err := json.NewDecoder(io.LimitReader(conn, gossipMaxBytes)).Decode(&theirs)
	<-sent
	if err != nil {
		return
	}
	if theirs.V != gossipVersion {
		return
	}
	nsCount := 0
	for ns, feeds := range theirs.Feeds {
		if nsCount++; nsCount > gossipMaxNamespaces {
			return
		}
		n.mu.Lock()
		if _, tracked := n.known[ns]; !tracked {
			n.mu.Unlock()
			continue
		}
		n.mu.Unlock()
		for i, id := range feeds {
			if i >= gossipMaxFeeds {
				break
			}
			n.AddFeed(ns, id)
		}
	}
}
