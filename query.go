package hearsay

import (
	"cmp"
	"fmt"
	"maps"
	"math"
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

	if observation, ok := h.store.truth[ns][key]; ok {
		age := max(now.Sub(observation.At), 0)
		if age <= policy.MaxAge {
			answer := Answer{
				Known:          true,
				Value:          observation.Value,
				Support:        1,
				EvidenceWeight: evidenceWeight(domain, age),
				Sources:        1,
				Age:            age,
				Coverage:       1,
				Local:          true,
			}
			h.store.mu.RUnlock()
			h.metrics.recordQuery(ns, answer)
			return answer, nil
		}
	}

	var score, total float64
	var covering, hits int
	minAge := time.Duration(-1)
	var scalarClaims []weightedClaim
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
		hits++
		if domain.ValueType() == Bool {
			score += weighted
			if minAge < 0 || age < minAge {
				minAge = age
			}
		} else {
			scalarClaims = append(scalarClaims, weightedClaim{value: value, weight: weighted, age: age})
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

	answer := Answer{Known: hits > 0}
	if len(knownFeeds) > 0 {
		answer.Coverage = float64(covering) / float64(len(knownFeeds))
	}
	if domain.ValueType() == Bool {
		answer.Sources = hits
		answer.EvidenceWeight = score
		if total > 0 {
			answer.Support = min(score/total, 1)
		}
		if minAge >= 0 {
			answer.Age = minAge
		}
		if score > 0 {
			answer.Value = 1
		}
	} else if len(scalarClaims) > 0 {
		answer.Value, answer.EvidenceWeight, answer.Sources, answer.Age = resolveScalar(scalarClaims)
		if total > 0 {
			answer.Support = min(answer.EvidenceWeight/total, 1)
		}
	}
	h.store.mu.RUnlock()
	h.metrics.recordQuery(ns, answer)
	return answer, nil
}

type weightedClaim struct {
	value  float64
	weight float64
	age    time.Duration
}

func resolveScalar(claims []weightedClaim) (value, evidence float64, sources int, age time.Duration) {
	slices.SortFunc(claims, func(a, b weightedClaim) int { return cmp.Compare(a.value, b.value) })
	var total float64
	for _, claim := range claims {
		total += claim.weight
	}
	half := total / 2
	var cumulative float64
	for i, claim := range claims {
		cumulative += claim.weight
		if cumulative < half {
			continue
		}
		value = claim.value
		if math.Abs(cumulative-half) <= 1e-12*max(total, 1) && i+1 < len(claims) {
			value = (claim.value + claims[i+1].value) / 2
		}
		break
	}
	age = -1
	for _, claim := range claims {
		if math.Abs(claim.value-value) > scalarAgreementTolerance {
			continue
		}
		evidence += claim.weight
		sources++
		if age < 0 || claim.age < age {
			age = claim.age
		}
	}
	if age < 0 {
		for _, claim := range claims {
			if age < 0 || claim.age < age {
				age = claim.age
			}
		}
	}
	return value, evidence, sources, age
}

func evidenceWeight(domain Domain, age time.Duration) float64 {
	return min(max(domain.Decay(age), 0), 1)
}
