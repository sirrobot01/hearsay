package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTopic(t *testing.T) {
	a := Topic("debrid.realdebrid.cached")
	b := Topic("debrid.alldebrid.cached")
	if a == b {
		t.Fatal("topics collide")
	}
	if a != Topic("debrid.realdebrid.cached") {
		t.Fatal("topic not deterministic")
	}
}

func TestPointerTarget(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	if pointerTarget(pub, "ns.a") == pointerTarget(pub, "ns.b") {
		t.Fatal("salt not applied")
	}
	pub2, _, _ := ed25519.GenerateKey(nil)
	if pointerTarget(pub, "ns.a") == pointerTarget(pub2, "ns.a") {
		t.Fatal("key not applied")
	}
}

func TestPointerSize(t *testing.T) {
	p := Pointer{Gen: 4412, InfoHash: strings.Repeat("f", 40), Size: 1216512, BuiltAt: 1754400000}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 1000 {
		t.Fatalf("pointer exceeds BEP 44 cap: %d bytes", len(raw))
	}
	var got Pointer
	if err := json.Unmarshal(raw, &got); err != nil || got != p {
		t.Fatalf("round-trip: %+v, %v", got, err)
	}
}

func TestGossipExchange(t *testing.T) {
	feedA := hex.EncodeToString(make([]byte, 32))
	b := make([]byte, 32)
	b[0] = 1
	feedB := hex.EncodeToString(b)

	n1 := &Node{known: map[string]map[string]struct{}{}}
	n2 := &Node{known: map[string]map[string]struct{}{}}
	n1.Track("ns.x")
	n1.AddFeed("ns.x", feedA)
	n2.Track("ns.x")
	n2.AddFeed("ns.x", feedB)
	n2.AddFeed("ns.untracked-by-n1", feedB)

	c1, c2 := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	c1.SetDeadline(deadline)
	c2.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		n2.talk(c2)
		c2.Close()
		close(done)
	}()
	n1.talk(c1)
	c1.Close()
	<-done

	if got := n1.Feeds("ns.x"); len(got) != 2 {
		t.Fatalf("n1 feeds = %v", got)
	}
	if got := n2.Feeds("ns.x"); len(got) != 2 {
		t.Fatalf("n2 feeds = %v", got)
	}
	if got := n1.Feeds("ns.untracked-by-n1"); len(got) != 0 {
		t.Fatal("untracked namespace accepted")
	}
	if n1.AddFeed("ns.x", "junk") {
		t.Fatal("invalid key accepted")
	}
}

// An allowlist must hold against the two paths that add feeds without
// the operator's say-so: gossip from any peer that connects, and the
// syncer re-registering whatever an earlier run left on disk.
func TestAllowlistRejectsGossipedFeeds(t *testing.T) {
	followed := hex.EncodeToString(make([]byte, 32))
	other := make([]byte, 32)
	other[0] = 1
	injected := hex.EncodeToString(other)

	n1 := &Node{known: map[string]map[string]struct{}{}}
	n1.Track("ns.x")
	n1.Allow(followed)
	if !n1.AddFeed("ns.x", followed) {
		t.Fatal("followed key rejected")
	}
	if n1.AddFeed("ns.x", injected) {
		t.Fatal("key outside the allowlist accepted")
	}

	n2 := &Node{known: map[string]map[string]struct{}{}}
	n2.Track("ns.x")
	n2.AddFeed("ns.x", injected)

	c1, c2 := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	c1.SetDeadline(deadline)
	c2.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		n2.talk(c2)
		c2.Close()
		close(done)
	}()
	n1.talk(c1)
	c1.Close()
	<-done

	if got := n1.Feeds("ns.x"); len(got) != 1 || got[0] != followed {
		t.Fatalf("gossip added a feed outside the allowlist: %v", got)
	}
}

// Discovery announces this node's address on a public topic, and with
// an allowlist it could not act on what it learns anyway.
func TestAllowlistDisablesDiscovery(t *testing.T) {
	n := &Node{known: map[string]map[string]struct{}{}}
	n.Track("ns.x")
	n.Allow(hex.EncodeToString(make([]byte, 32)))
	// gossip is nil, so Discover panics on the announce path if the
	// allowlist does not short-circuit it first.
	n.Discover(context.Background(), "ns.x")
}

// TestPeerCountExcludesSelf checks the announced self key still reaches
// gossip but is left out of the peer count, which otherwise overstates
// every namespace by one.
func TestPeerCountExcludesSelf(t *testing.T) {
	self := hex.EncodeToString(make([]byte, 32))
	other := make([]byte, 32)
	other[0] = 1
	peer := hex.EncodeToString(other)

	n := &Node{known: map[string]map[string]struct{}{}}
	n.Track("ns.x")
	n.Track("ns.empty")
	n.Announce("ns.x", self)
	n.Announce("ns.empty", self)
	n.AddFeed("ns.x", peer)

	n.mu.Lock()
	gotX, gotEmpty := n.peersLocked("ns.x"), n.peersLocked("ns.empty")
	n.mu.Unlock()
	if gotX != 1 {
		t.Errorf("ns.x peers = %d, want 1", gotX)
	}
	if gotEmpty != 0 {
		t.Errorf("ns.empty peers = %d, want 0", gotEmpty)
	}
	// Gossip must still advertise self, or peers never learn this feed.
	if got := n.Feeds("ns.x"); len(got) != 2 {
		t.Errorf("gossip feeds = %v, want self and peer", got)
	}
}
