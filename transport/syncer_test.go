package transport

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/debrid"
)

// TestSyncCoversEveryNamespace runs a full sync cycle with no DHT
// servers, so pointer lookups fail fast and the cycle exercises only
// the fan-out. Namespaces sync concurrently, so this also stands as
// the race check for the shared feed table.
func TestSyncCoversEveryNamespace(t *testing.T) {
	dir := t.TempDir()
	var domains []hearsay.Domain
	var nss []string
	for _, p := range debrid.Providers {
		d := debrid.New(p)
		domains = append(domains, d)
		nss = append(nss, d.Namespace())
	}

	consumer, err := hearsay.New(filepath.Join(dir, "consumer"), domains...)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	publisher, err := hearsay.New(filepath.Join(dir, "publisher"), domains...)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	// One retained generation per namespace, so every namespace has a
	// feed the cycle must register and then attempt to pull.
	for i, ns := range nss {
		subject := fmt.Sprintf("%040x", i+1)
		if err := publisher.Observe(ns, subject, 1, time.Now()); err != nil {
			t.Fatal(err)
		}
		g, err := publisher.Publish(ns)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := g.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := consumer.Ingest(raw); err != nil {
			t.Fatal(err)
		}
	}

	gossip, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gossip.Close()
	n := &Node{known: map[string]map[string]*feedState{}, gossip: gossip}
	for _, ns := range nss {
		n.Track(ns)
	}

	s := &Syncer{
		Engine: consumer,
		Node:   n,
		Log:    slog.New(slog.DiscardHandler),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s.sync(ctx)

	feedID := hex.EncodeToString(publisher.Identity())
	selfID := hex.EncodeToString(consumer.Identity())
	for _, ns := range nss {
		got := n.Feeds(ns)
		if !slices.Contains(got, feedID) {
			t.Errorf("%s: retained feed not registered, got %v", ns, got)
		}
		if slices.Contains(got, selfID) {
			t.Errorf("%s: own feed registered as a source", ns)
		}
	}
}

// TestSyncStopsOnCancel checks the fan-out honours cancellation rather
// than running every namespace to completion after shutdown starts.
func TestSyncStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	d := debrid.New(debrid.RealDebrid)
	engine, err := hearsay.New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	gossip, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gossip.Close()
	n := &Node{known: map[string]map[string]*feedState{}, gossip: gossip}
	n.Track(d.Namespace())

	s := &Syncer{Engine: engine, Node: n, Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		s.sync(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not return on a cancelled context")
	}
}

func TestEmptyPublisherDoesNotAdvertiseFeed(t *testing.T) {
	d := debrid.New(debrid.RealDebrid)
	engine, err := hearsay.New(t.TempDir(), d)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	n := &Node{known: map[string]map[string]*feedState{}}
	s := &Syncer{
		Engine:  engine,
		Node:    n,
		Publish: true,
		Log:     slog.New(slog.DiscardHandler),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.Run(ctx)

	if feeds := n.Feeds(d.Namespace()); len(feeds) != 0 {
		t.Fatalf("empty publisher advertised feeds: %v", feeds)
	}
}
