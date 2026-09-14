package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/swarm"
	"github.com/sirrobot01/hearsay/usenet"
)

func TestConfigDefaultsPublishAllNamespaces(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hearsay.Participates() || !cfg.Hearsay.Publishes() {
		t.Fatalf("network defaults disabled: %+v", cfg.Hearsay)
	}
	if !slices.Equal(cfg.Adapters.Debrid, debrid.Providers) ||
		!slices.Equal(cfg.Adapters.Usenet, usenet.Backbones) || !cfg.Adapters.Torrent {
		t.Fatalf("adapter defaults = %+v", cfg.Adapters)
	}
	domains := domainsFromConfig(cfg)
	want := 2*len(debrid.Providers) + len(usenet.Backbones) + 1
	if len(domains) != want || domains[len(domains)-1].Namespace() != swarm.Namespace {
		t.Fatalf("domains = %v", domains)
	}
}

func TestConfigHonorsExplicitNetworkAndAdapterOptOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("hearsay:\n  participate: false\n  publish: false\nadapters:\n  debrid: []\n  usenet: []\n  torrent: false\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hearsay.Participates() || cfg.Hearsay.Publishes() {
		t.Fatalf("config = %+v", cfg)
	}
	if len(domainsFromConfig(cfg)) != 0 {
		t.Fatalf("adapters enabled: %+v", cfg.Adapters)
	}
}

func TestConfigSupportsReaderRelayMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hearsay:\n  publish: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hearsay.Participates() || cfg.Hearsay.Publishes() {
		t.Fatalf("config = %+v", cfg.Hearsay)
	}
}

func TestConfigAcceptsSeededTorrentLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hearsay:\n  max_seeded_torrents: 64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hearsay.MaxSeeded != 64 {
		t.Fatalf("seeded torrent limit = %d, want 64", cfg.Hearsay.MaxSeeded)
	}
}

func TestLegacyConfigRetainsNetworkDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hearsay:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hearsay.Participates() || !cfg.Hearsay.Publishes() {
		t.Fatalf("legacy network defaults lost: %+v", cfg.Hearsay)
	}
	if !slices.Equal(cfg.Adapters.Debrid, debrid.Providers) ||
		!slices.Equal(cfg.Adapters.Usenet, usenet.Backbones) || !cfg.Adapters.Torrent {
		t.Fatalf("legacy adapter defaults lost: %+v", cfg.Adapters)
	}
}

func TestLegacyNetworkOptOutDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hearsay:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hearsay.Participates() || cfg.Hearsay.Publishes() {
		t.Fatalf("legacy network opt-out lost: %+v", cfg.Hearsay)
	}
}

func TestFollowRejectsNonHexKey(t *testing.T) {
	if _, err := (Follow{Pubkey: strings.Repeat("g", 64)}).Key(); err == nil {
		t.Fatal("non-hex publisher key accepted")
	}
}

func TestConfigRejectsAmbiguousParticipation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("hearsay:\n  participate: false\n  enabled: true\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("ambiguous participation config accepted")
	}
}
