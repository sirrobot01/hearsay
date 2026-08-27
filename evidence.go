package hearsay

import (
	"cmp"
	"fmt"
)

// EvidenceState is the resolved state of complementary Boolean namespaces.
type EvidenceState string

const (
	EvidenceUnknown  EvidenceState = "unknown"
	EvidencePositive EvidenceState = "positive"
	EvidenceNegative EvidenceState = "negative"
)

// PairedAnswer preserves both sides of a complementary Boolean decision.
type PairedAnswer struct {
	State    EvidenceState
	Positive Answer
	Negative Answer
}

// QueryPair resolves a positive namespace and its complementary negative namespace.
func (h *Hearsay) QueryPair(positiveNS, negativeNS string, subject any) (PairedAnswer, error) {
	positiveDomain, positiveOK := h.domains[positiveNS]
	negativeDomain, negativeOK := h.domains[negativeNS]
	if !positiveOK || !negativeOK || positiveDomain.ValueType() != Bool || negativeDomain.ValueType() != Bool {
		return PairedAnswer{}, fmt.Errorf("hearsay: paired evidence requires two Boolean namespaces")
	}
	positiveRaw, err := h.Query(positiveNS, subject)
	if err != nil {
		return PairedAnswer{}, err
	}
	negativeRaw, err := h.Query(negativeNS, subject)
	if err != nil {
		return PairedAnswer{}, err
	}

	var positive, negative Answer
	if positiveRaw.Known {
		if positiveRaw.Value >= 0.5 {
			positive = normalizedEvidence(positiveRaw, 1)
		} else {
			negative = normalizedEvidence(positiveRaw, 0)
		}
	}
	if negativeRaw.Known {
		if negativeRaw.Value >= 0.5 {
			negative = chooseEvidence(negative, normalizedEvidence(negativeRaw, 0))
		} else {
			positive = chooseEvidence(positive, normalizedEvidence(negativeRaw, 1))
		}
	}

	paired := PairedAnswer{Positive: positive, Negative: negative}
	switch compareEvidence(positive, negative) {
	case 1:
		paired.State = EvidencePositive
	case -1:
		paired.State = EvidenceNegative
	default:
		paired.State = EvidenceUnknown
	}
	return paired, nil
}

func normalizedEvidence(answer Answer, value float64) Answer {
	answer.Value = value
	return answer
}

func chooseEvidence(a, b Answer) Answer {
	if compareEvidence(a, b) >= 0 {
		return a
	}
	return b
}

func compareEvidence(a, b Answer) int {
	if a.Known != b.Known {
		if a.Known {
			return 1
		}
		return -1
	}
	if !a.Known {
		return 0
	}
	if a.Local != b.Local {
		if a.Local {
			return 1
		}
		return -1
	}
	if order := cmp.Compare(a.EvidenceWeight, b.EvidenceWeight); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Support, b.Support); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Sources, b.Sources); order != 0 {
		return order
	}
	return cmp.Compare(b.Age, a.Age)
}
