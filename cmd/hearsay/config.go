package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sirrobot01/hearsay/debrid"
	"github.com/sirrobot01/hearsay/usenet"
)

// Config is fully functional with every field absent: zero-config is
// a hard requirement.
type Config struct {
	Dir     string `yaml:"dir"`
	Listen  string `yaml:"listen"`
	Hearsay struct {
		Enabled    bool     `yaml:"enabled"`
		Publish    bool     `yaml:"publish"`
		Interval   string   `yaml:"interval"`
		Port       int      `yaml:"port"`
		GossipPort int      `yaml:"gossip_port"`
		Follow     []Follow `yaml:"follow"`
	} `yaml:"hearsay"`
	Adapters struct {
		Debrid []string `yaml:"debrid"`
		Usenet []string `yaml:"usenet"`
	} `yaml:"adapters"`
}

type Follow struct {
	Pubkey string `yaml:"pubkey"`
	Label  string `yaml:"label"`
}

// defaultConfigPath is where serve looks without --config, e.g.
// ~/.config/hearsay/config.yaml on Linux. A missing file means
// defaults: zero-config stays zero-config.
func defaultConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "hearsay", "config.yaml")
}

func loadConfig(path string) (Config, error) {
	var c Config
	c.Hearsay.Enabled = true
	c.Hearsay.Publish = true
	raw, err := os.ReadFile(path)
	if err != nil && !(path == "" || errors.Is(err, fs.ErrNotExist)) {
		return c, err
	}
	if err == nil {
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
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
	// Defaults come from the adapter packages so the daemon tracks
	// every namespace the library can publish.
	if c.Adapters.Debrid == nil {
		c.Adapters.Debrid = debrid.Providers
	}
	if c.Adapters.Usenet == nil {
		c.Adapters.Usenet = usenet.Backbones
	}
	return c, nil
}

// Key parses the configured "ed25519:<hex>" or bare hex public key.
func (f Follow) Key() (string, error) {
	k := strings.TrimPrefix(strings.TrimSpace(f.Pubkey), "ed25519:")
	if len(k) != 64 {
		return "", fmt.Errorf("follow %q: want 64 hex chars", f.Pubkey)
	}
	return strings.ToLower(k), nil
}
