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
	"net"
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
	"github.com/anacrolix/torrent/metainfo"
)

// MaxGenerationSize bounds fetched blobs: a bloom filter for a
// million subjects is about 1.2MB, so this is generous.
const MaxGenerationSize = 64 << 20

// DefaultMaxStorage bounds generation blobs retained for relay.
const DefaultMaxStorage = 1 << 30

const dialCap = 8

type Node struct {
	dir        string
	client     *torrent.Client
	dhts       []*dht.Server
	gossip     net.Listener
	maxStorage int64

	mu        sync.Mutex
	storageMu sync.Mutex
	self      string
	known     map[string]map[string]struct{}
	allow     map[string]struct{} // nil = accept any discovered key
}

type NodeConfig struct {
	Dir        string
	Port       int
	GossipPort int
	MaxStorage int64
}

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
	if err := os.MkdirAll(config.Dir, 0o755); err != nil {
		return nil, err
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = config.Dir
	cfg.Seed = true
	cfg.ListenPort = config.Port
	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	n := &Node{
		dir:        config.Dir,
		client:     client,
		maxStorage: config.MaxStorage,
		known:      map[string]map[string]struct{}{},
	}
	if n.maxStorage == 0 {
		n.maxStorage = DefaultMaxStorage
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
	return n, nil
}

func (n *Node) Close() error {
	n.gossip.Close()
	n.client.Close()
	return nil
}

// Track opens a namespace for gossip: feeds offered for untracked
// namespaces are ignored.
func (n *Node) Track(ns string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.known[ns] == nil {
		n.known[ns] = map[string]struct{}{}
	}
}

// Announce registers this node's own publisher key for a namespace so
// gossip has something to spread, and records it as self so peer
// counts can leave it out.
func (n *Node) Announce(ns, self string) bool {
	n.mu.Lock()
	n.self = self
	n.mu.Unlock()
	return n.AddFeed(ns, self)
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
}

// AddFeed registers a publisher key for a namespace, from config or
// from discovery. Keys are candidates only: their pointers and
// generations still verify cryptographically, and their weight is
// earned through outcomes. An allowlist rejects everything outside it.
func (n *Node) AddFeed(ns, pubHex string) bool {
	if raw, err := hex.DecodeString(pubHex); err != nil || len(raw) != 32 {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.allow != nil {
		if _, ok := n.allow[pubHex]; !ok {
			return false
		}
	}
	if n.known[ns] == nil {
		n.known[ns] = map[string]struct{}{}
	}
	if _, ok := n.known[ns][pubHex]; ok {
		return false
	}
	n.known[ns][pubHex] = struct{}{}
	return true
}

// Feeds lists known publisher keys for a namespace.
func (n *Node) Feeds(ns string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := make([]string, 0, len(n.known[ns]))
	for id := range n.known[ns] {
		ids = append(ids, id)
	}
	return ids
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
	SwarmPort   int            `json:"swarm_port"`
	GossipPort  int            `json:"gossip_port"`
	StoredBytes int64          `json:"stored_bytes"`
	MaxStorage  int64          `json:"max_storage_bytes"`
	Torrents    []TorrentStat  `json:"torrents"`
	Feeds       map[string]int `json:"feeds"`
}

func (n *Node) Stats() Stats {
	s := Stats{SwarmPort: n.client.LocalPort(), MaxStorage: n.maxStorage, Feeds: map[string]int{}}
	if a, ok := n.gossip.Addr().(*net.TCPAddr); ok {
		s.GossipPort = a.Port
	}
	for _, t := range n.client.Torrents() {
		st := TorrentStat{
			InfoHash:    t.InfoHash().HexString(),
			ActivePeers: t.Stats().ActivePeers,
		}
		if t.Info() != nil {
			st.Size = t.Length()
			st.Completed = t.BytesCompleted()
		}
		s.Torrents = append(s.Torrents, st)
	}
	s.StoredBytes = n.storageUsage()
	n.mu.Lock()
	for ns := range n.known {
		s.Feeds[ns] = n.peersLocked(ns)
	}
	n.mu.Unlock()
	return s
}

// peersLocked counts the publisher keys known for a namespace without
// this node's own. Announce registers self so gossip has something to
// spread, but sync never pulls from it, so reporting it as a peer
// overstates the network by one in every namespace. Caller holds n.mu.
func (n *Node) peersLocked(ns string) int {
	c := len(n.known[ns])
	if _, ok := n.known[ns][n.self]; ok && n.self != "" {
		c--
	}
	return c
}

// Seed writes a generation blob into the swarm directory and serves
// it as a single-file torrent.
func (n *Node) Seed(blob []byte) (metainfo.Hash, error) {
	n.storageMu.Lock()
	defer n.storageMu.Unlock()

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
	t, _ := n.client.AddTorrentInfoHash(ih)
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		t.Drop()
		return nil, ctx.Err()
	}
	if t.Length() > MaxGenerationSize || t.Info().IsDir() {
		t.Drop()
		return nil, fmt.Errorf("transport: not a generation blob: %d bytes", t.Length())
	}
	t.DownloadAll()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for t.BytesCompleted() < t.Length() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Drop()
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
	if name != "" {
		now := time.Now()
		if err := os.Chtimes(filepath.Join(n.dir, name), now, now); err != nil {
			n.storageMu.Unlock()
			return nil, err
		}
	}
	err = n.pruneStorageLocked(name)
	n.storageMu.Unlock()
	return raw, err
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
					if t.Stats().ActivePeers > 0 || t.BytesCompleted() < t.Length() {
						inUse = true
						break
					}
					t.Drop()
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
					addr := net.JoinHostPort(peer.IP.String(), strconv.Itoa(peer.Port))
					if _, dup := seen[addr]; dup || peer.Port == 0 {
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
	msg := gossipMsg{V: 1, Feeds: make(map[string][]string, len(n.known))}
	for ns, feeds := range n.known {
		for id := range feeds {
			msg.Feeds[ns] = append(msg.Feeds[ns], id)
		}
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
