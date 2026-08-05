package hearsay

import "time"

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

	// Decay is the confidence multiplier for an observation of the
	// given age, in [0, 1].
	Decay(age time.Duration) float64

	// Verify converts the outcome of acting on a hint into ground
	// truth. An unknown Truth means the outcome revealed nothing.
	Verify(subject any, outcome any) Truth
}
