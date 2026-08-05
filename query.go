package hearsay

import (
	"fmt"
	"time"
)

// Answer is never a bare boolean: callers set their own thresholds.
// For Bool domains Value is 0 or 1; for Scalar domains it is the
// weighted mean of source claims. Confidence is the decayed,
// reputation-weighted share of covering sources that back the value.
// Coverage is the share of known feeds that publish this namespace.
// Local reports authoritative ground truth, not a hint.
type Answer struct {
	Value      float64
	Confidence float64
	Sources    int
	Age        time.Duration
	Coverage   float64
	Local      bool
}

// Query answers from local ground truth if present, otherwise from
// retained generations of followed feeds, weighted by per-source
// reputation and domain decay.
func (h *Hearsay) Query(ns string, subject any) (Answer, error) {
	d, ok := h.domains[ns]
	if !ok {
		return Answer{}, fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	key, err := d.Encode(subject)
	if err != nil {
		return Answer{}, err
	}
	now := time.Now()

	h.store.mu.RLock()
	defer h.store.mu.RUnlock()

	if o, ok := h.store.truth[ns][key]; ok {
		return Answer{
			Value:      o.Value,
			Confidence: 1,
			Sources:    1,
			Age:        now.Sub(o.At),
			Coverage:   1,
			Local:      true,
		}, nil
	}

	var score, total, valueSum float64
	var covering, hits int
	minAge := time.Duration(-1)
	for feed, gens := range h.store.feeds[ns] {
		if feed == h.self {
			continue
		}
		covering++
		w := h.rep.weight(feed)
		newest := gens[len(gens)-1]
		total += w * min(max(d.Decay(now.Sub(newest.BuiltAt)), 0), 1)
		for i := len(gens) - 1; i >= 0; i-- {
			v, ok := payloadLookup(gens[i].Type, gens[i].Payload, key)
			if !ok {
				continue
			}
			age := now.Sub(gens[i].BuiltAt)
			dw := w * min(max(d.Decay(age), 0), 1)
			score += dw
			valueSum += dw * v
			hits++
			if minAge < 0 || age < minAge {
				minAge = age
			}
			break
		}
	}

	known := map[string]struct{}{}
	for _, byFeed := range h.store.feeds {
		for id := range byFeed {
			if id != h.self {
				known[id] = struct{}{}
			}
		}
	}

	a := Answer{Sources: hits}
	if len(known) > 0 {
		a.Coverage = float64(covering) / float64(len(known))
	}
	if total > 0 {
		a.Confidence = score / total
	}
	if minAge >= 0 {
		a.Age = minAge
	}
	if score > 0 {
		if d.ValueType() == Bool {
			if a.Confidence >= 0.5 {
				a.Value = 1
			}
		} else {
			a.Value = valueSum / score
		}
	}
	return a, nil
}
