// Package swarm provides a domain for public-torrent availability.
// Hosts report only completed or terminal download attempts; live samples,
// user cancellations, and active probing are not observations.
package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sirrobot01/hearsay"
)

// Namespace identifies public-torrent terminal availability observations.
const Namespace = "torrent.public.availability"

// Domain shares recent terminal download availability by infohash.
type Domain struct{}

// New returns the public-torrent availability domain.
func New() Domain {
	return Domain{}
}

func (Domain) Namespace() string {
	return Namespace
}

// Encode accepts a v1 or v2 infohash as hexadecimal text or raw bytes.
func (Domain) Encode(subject any) (hearsay.Key, error) {
	var raw []byte
	switch subject := subject.(type) {
	case string:
		decoded, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(subject)))
		if err != nil {
			return hearsay.Key{}, fmt.Errorf("swarm: not an infohash: %q", subject)
		}
		raw = decoded
	case [20]byte:
		raw = subject[:]
	case [32]byte:
		raw = subject[:]
	default:
		return hearsay.Key{}, fmt.Errorf("swarm: unsupported subject type %T", subject)
	}
	if len(raw) != 20 && len(raw) != 32 {
		return hearsay.Key{}, fmt.Errorf("swarm: infohash must be 20 or 32 bytes")
	}
	return sha256.Sum256(raw), nil
}

func (Domain) ValueType() hearsay.ValueType {
	return hearsay.Scalar
}

func (Domain) Decay(age time.Duration) float64 {
	return math.Exp2(-age.Hours() / 12)
}

func (Domain) Freshness() hearsay.FreshnessPolicy {
	return hearsay.FreshnessPolicy{MaxAge: 72 * time.Hour}
}

// Verify accepts a terminal completion ratio or a final success value.
func (Domain) Verify(_ any, outcome any) hearsay.Truth {
	switch outcome := outcome.(type) {
	case float64:
		return hearsay.Truth{Known: true, Value: min(max(outcome, 0), 1)}
	case bool:
		if outcome {
			return hearsay.Truth{Known: true, Value: 1}
		}
		return hearsay.Truth{Known: true, Value: 0}
	default:
		return hearsay.Truth{}
	}
}
