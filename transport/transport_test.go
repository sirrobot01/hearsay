package transport

import (
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
