package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tokenFile is the daemon's API token, kept beside the identity key.
const tokenFile = "token"

// loadToken reads the API token, generating it on first run. The API
// writes local ground truth and the daemon publishes that to the
// network, so every endpoint requires the token: a loopback bind is not
// a boundary against the browser the operator is already running.
func loadToken(dir string) (string, error) {
	path := filepath.Join(dir, tokenFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		token := hex.EncodeToString(b)
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			return "", err
		}
		return token, nil
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("hearsay: empty token file %s: delete it to generate a new one", path)
	}
	return token, nil
}

// clientToken finds the token for talking to a local daemon: the flag,
// then HEARSAY_TOKEN, then the file the daemon wrote. Reading the file
// keeps same-host commands zero-config.
func clientToken(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("HEARSAY_TOKEN"); env != "" {
		return env, nil
	}
	cfg, err := loadConfig(defaultConfigPath())
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Dir, tokenFile))
	if err != nil {
		return "", fmt.Errorf("cannot read the daemon token; pass --token or set HEARSAY_TOKEN: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
