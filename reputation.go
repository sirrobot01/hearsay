package hearsay

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
)

// reputation is the local, private trust ledger. Weights are derived
// exclusively from verified outcomes of acting on hints: no global
// reputation, no imported scores. New sources start at 0.5 and move by
// EWMA toward their observed accuracy.
type reputation struct {
	mu   sync.Mutex
	path string
	w    map[string]float64
}

const repAlpha = 0.2

func openReputation(path string) (*reputation, error) {
	r := &reputation{path: path, w: map[string]float64{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &r.w); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *reputation) weight(feed string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.w[feed]; ok {
		return w
	}
	return 0.5
}

func (r *reputation) record(feed string, agree bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.w[feed]
	if !ok {
		w = 0.5
	}
	outcome := 0.0
	if agree {
		outcome = 1
	}
	r.w[feed] = (1-repAlpha)*w + repAlpha*outcome
	raw, err := json.MarshalIndent(r.w, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.path+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(r.path+".tmp", r.path)
}
