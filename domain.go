package hearsay

import (
	"strings"
	"time"
)

// Key is a derived subject identifier. Adapters must derive it
// deterministically so that independent implementations agree on the
// key for the same subject with no coordination.
type Key [32]byte

// ValueType selects the payload encoding for a namespace.
type ValueType uint8

const (
	Bool ValueType = iota
	Scalar
	Enum
)

// Truth is ground truth revealed as a consequence of acting on a hint.
type Truth struct {
	Known bool
	Value float64
}

// FreshnessPolicy bounds evidence lifetime. Boolean observations are
// grouped into Bucket intervals so their age survives Bloom encoding.
type FreshnessPolicy struct {
	Bucket time.Duration
	MaxAge time.Duration
}

// Domain is a pluggable observation domain. It deliberately exposes no
// method that queries the observed system: adapters only receive
// observations that fall out of work the host application was doing
// anyway. Probing to generate data is a protocol violation.
type Domain interface {
	// Namespace is the stable reverse-DNS identifier, e.g.
	// "debrid.realdebrid.cached".
	Namespace() string

	// Encode derives the subject key. Must be deterministic across
	// implementations.
	Encode(subject any) (Key, error)

	// ValueType drives payload encoding. The core never inspects
	// payload semantics.
	ValueType() ValueType

	// Decay is the evidence multiplier for an observation of the
	// given age, in [0, 1].
	Decay(age time.Duration) float64

	// Freshness defines claim-age encoding and expiry.
	Freshness() FreshnessPolicy

	// Verify converts the outcome of acting on a hint into ground
	// truth. An unknown Truth means the outcome revealed nothing.
	Verify(subject any, outcome any) Truth
}

func validNamespace(namespace string) bool {
	if len(namespace) == 0 || len(namespace) > 59 || strings.HasPrefix(namespace, ".") ||
		strings.HasSuffix(namespace, ".") || strings.Contains(namespace, "..") {
		return false
	}
	for _, char := range namespace {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
