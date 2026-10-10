package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/usenet"
)

// Config preserves the standalone daemon's zero-config network behavior.
type Config struct {
	Dir string `yaml:"dir"`
	// Listen is the HTTP API address. Keep it on loopback unless the
	// network in front of it is trusted.
	Listen string `yaml:"listen"`
	// AllowedHosts adds Host header values the API accepts, for a
	// reverse proxy that forwards its own name. IP literals and
	// localhost are always accepted.
	AllowedHosts []string      `yaml:"allowed_hosts"`
	Hearsay      NetworkConfig `yaml:"hearsay"`
	Adapters     struct {
		Debrid  []string `yaml:"debrid"`
		Usenet  []string `yaml:"usenet"`
		Torrent bool     `yaml:"torrent"`
	} `yaml:"adapters"`
}

type NetworkConfig struct {
	Participate *bool    `yaml:"participate"`
	Enabled     *bool    `yaml:"enabled"`
	Publish     *bool    `yaml:"publish"`
	Interval    string   `yaml:"interval"`
	Port        int      `yaml:"port"`
	GossipPort  int      `yaml:"gossip_port"`
	MaxStorage  int64    `yaml:"max_storage_bytes"`
	MaxFeeds    int      `yaml:"max_feeds_per_namespace"`
	MaxSeeded   int      `yaml:"max_seeded_torrents"`
	Follow      []Follow `yaml:"follow"`
	// BlockPrivatePeers skips peers in transport.PrivatePeerPrefixes.
	BlockPrivatePeers bool     `yaml:"block_private_peers"`
	Blocklist         []string `yaml:"blocklist"`
}

func (c NetworkConfig) Participates() bool {
	if c.Participate != nil {
		return *c.Participate
	}
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

func (c NetworkConfig) Publishes() bool {
	return c.Participates() && (c.Publish == nil || *c.Publish)
}

type Follow struct {
	Pubkey string `yaml:"pubkey"`
	Label  string `yaml:"label"`
}

// defaultConfigPath is where serve looks without --config, e.g.
// ~/.config/hearsay/config.yaml on Linux. A missing file uses the
// standalone daemon defaults.
func defaultConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "hearsay", "config.yaml")
}

func loadConfig(path string) (Config, error) {
	var c Config
	c.Adapters.Torrent = true
	raw, err := os.ReadFile(path)
	if err != nil && !(path == "" || errors.Is(err, fs.ErrNotExist)) {
		return c, err
	}
	if err == nil {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
		if c.Hearsay.Participate != nil && c.Hearsay.Enabled != nil {
			return c, fmt.Errorf("parse %s: use hearsay.participate, not both participate and enabled", path)
		}
	}
	if c.Dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return c, err
		}
		c.Dir = filepath.Join(base, "hearsay")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8477"
	}
	if c.Adapters.Debrid == nil {
		c.Adapters.Debrid = slices.Clone(debrid.Providers)
	}
	if c.Adapters.Usenet == nil {
		c.Adapters.Usenet = slices.Clone(usenet.Backbones)
	}
	return c, nil
}

// Key parses the configured "ed25519:<hex>" or bare hex public key.
func (f Follow) Key() (string, error) {
	k := strings.TrimPrefix(strings.TrimSpace(f.Pubkey), "ed25519:")
	if raw, err := hex.DecodeString(k); err != nil || len(raw) != 32 {
		return "", fmt.Errorf("follow %q: want 64 hex chars", f.Pubkey)
	}
	return strings.ToLower(k), nil
}
