package hearsay

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Answer separates whether evidence exists from its value. Support is
// corroborating source weight, while EvidenceWeight is its absolute mass.
type Answer struct {
	Known          bool
	Value          float64
	Support        float64
	EvidenceWeight float64
	Sources        int
	Age            time.Duration
	Coverage       float64
	Local          bool
}

func (h *Hearsay) Query(ns string, subject any) (Answer, error) {
	domain, ok := h.domains[ns]
	if !ok {
		return Answer{}, fmt.Errorf("hearsay: unknown namespace %q", ns)
	}
	key, err := domain.Encode(subject)
	if err != nil {
		return Answer{}, err
	}
	now := time.Now()
	policy := domain.Freshness()

	h.store.mu.RLock()
	defer h.store.mu.RUnlock()

	if observation, ok := h.store.truth[ns][key]; ok {
		age := max(now.Sub(observation.At), 0)
		if age <= policy.MaxAge {
			return Answer{
				Known:          true,
				Value:          observation.Value,
				Support:        1,
				EvidenceWeight: evidenceWeight(domain, age),
				Sources:        1,
				Age:            age,
				Coverage:       1,
				Local:          true,
			}, nil
		}
	}

	var score, total, valueSum float64
	var covering, hits int
	minAge := time.Duration(-1)
	feedIDs := slices.Collect(maps.Keys(h.store.feeds[ns]))
	feedIDs = slices.DeleteFunc(feedIDs, func(feed string) bool { return feed == h.self })
	weights := h.rep.weights(feedIDs)
	for feed, generations := range h.store.feeds[ns] {
		if feed == h.self {
			continue
		}
		weight := weights[feed]
		newestAge := max(now.Sub(generations[len(generations)-1].BuiltAt), 0)
		if newestAge <= policy.MaxAge {
			covering++
			total += weight * evidenceWeight(domain, newestAge)
		}
		newest := generations[len(generations)-1]
		value, observedAt, ok := payloadLookup(newest.Type, newest.Payload, key)
		if !ok {
			continue
		}
		age := max(now.Sub(observedAt), 0)
		if age > policy.MaxAge {
			continue
		}
		weighted := weight * evidenceWeight(domain, age)
		score += weighted
		valueSum += weighted * value
		hits++
		if minAge < 0 || age < minAge {
			minAge = age
		}
	}

	knownFeeds := map[string]struct{}{}
	for _, byFeed := range h.store.feeds {
		for id := range byFeed {
			if id != h.self {
				knownFeeds[id] = struct{}{}
			}
		}
	}

	answer := Answer{Known: hits > 0, Sources: hits, EvidenceWeight: score}
	if len(knownFeeds) > 0 {
		answer.Coverage = float64(covering) / float64(len(knownFeeds))
	}
	if total > 0 {
		answer.Support = min(score/total, 1)
	}
	if minAge >= 0 {
		answer.Age = minAge
	}
	if score > 0 {
		if domain.ValueType() == Bool {
			answer.Value = 1
		} else {
			answer.Value = valueSum / score
		}
	}
	return answer, nil
}

func evidenceWeight(domain Domain, age time.Duration) float64 {
	return min(max(domain.Decay(age), 0), 1)
}
