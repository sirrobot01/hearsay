package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirrobot01/hearsay/swarm"
)

func TestConfigDefaultsAreLocalAndInactive(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hearsay.Participates() || cfg.Hearsay.Publish {
		t.Fatalf("network enabled by default: %+v", cfg.Hearsay)
	}
	if len(cfg.Adapters.Debrid) != 0 || len(cfg.Adapters.Usenet) != 0 || cfg.Adapters.Torrent {
		t.Fatalf("adapters enabled by default: %+v", cfg.Adapters)
	}
}

func TestConfigRequiresExplicitParticipation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("hearsay:\n  participate: true\n  publish: true\nadapters:\n  debrid: [realdebrid]\n  torrent: true\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hearsay.Participates() || !cfg.Hearsay.Publish || len(cfg.Adapters.Debrid) != 1 || !cfg.Adapters.Torrent {
		t.Fatalf("config = %+v", cfg)
	}
	domains := domainsFromConfig(cfg)
	if len(domains) != 3 || domains[2].Namespace() != swarm.Namespace {
		t.Fatalf("domains = %v", domains)
	}
}

func TestLegacyEnabledStillOptsIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hearsay:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hearsay.Participates() {
		t.Fatal("legacy explicit opt-in was lost")
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
