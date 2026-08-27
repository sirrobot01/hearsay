package hearsay

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"sync"
)

const (
	probationWeight      = 0.05
	unverifiedPoolWeight = 0.25
	wilsonZ              = 1.645
	legacySamples        = 10
)

type sourceReputation struct {
	Successes uint64 `json:"successes"`
	Failures  uint64 `json:"failures"`
}

func (s sourceReputation) samples() uint64 {
	return s.Successes + s.Failures
}

func (s sourceReputation) weight() float64 {
	n := s.samples()
	if n == 0 {
		return probationWeight
	}
	observations := float64(n)
	accuracy := float64(s.Successes) / observations
	zSquared := wilsonZ * wilsonZ
	center := accuracy + zSquared/(2*observations)
	margin := wilsonZ * math.Sqrt((accuracy*(1-accuracy)+zSquared/(4*observations))/observations)
	lower := (center - margin) / (1 + zSquared/observations)
	return max(lower, 0.005)
}

type reputation struct {
	mu   sync.Mutex
	path string
	byID map[string]sourceReputation
}

func openReputation(path string) (*reputation, error) {
	r := &reputation{path: path, byID: map[string]sourceReputation{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	for id, encoded := range entries {
		var source sourceReputation
		if err := json.Unmarshal(encoded, &source); err == nil {
			r.byID[id] = source
			continue
		}
		var legacy float64
		if err := json.Unmarshal(encoded, &legacy); err != nil {
			return nil, err
		}
		successes := uint64(math.Round(min(max(legacy, 0), 1) * legacySamples))
		r.byID[id] = sourceReputation{Successes: successes, Failures: legacySamples - successes}
	}
	return r, nil
}

func (r *reputation) weight(feed string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[feed].weight()
}

func (r *reputation) weights(feeds []string) map[string]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	weights := make(map[string]float64, len(feeds))
	var probationTotal float64
	for _, feed := range feeds {
		source := r.byID[feed]
		weight := source.weight()
		weights[feed] = weight
		if source.samples() == 0 {
			probationTotal += weight
		}
	}
	if probationTotal <= unverifiedPoolWeight {
		return weights
	}
	scale := unverifiedPoolWeight / probationTotal
	for _, feed := range feeds {
		if r.byID[feed].samples() == 0 {
			weights[feed] *= scale
		}
	}
	return weights
}

func (r *reputation) record(feed string, agree bool) error {
	return r.recordMany(map[string]bool{feed: agree})
}

func (r *reputation) recordMany(outcomes map[string]bool) error {
	if len(outcomes) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for feed, agree := range outcomes {
		source := r.byID[feed]
		if agree {
			source.Successes++
		} else {
			source.Failures++
		}
		r.byID[feed] = source
	}
	return r.saveLocked()
}

func (r *reputation) saveLocked() error {
	raw, err := json.MarshalIndent(r.byID, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.path+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(r.path+".tmp", r.path)
}
