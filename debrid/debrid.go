// Package debrid provides Hearsay domains for debrid service cache
// availability. Subjects are torrent infohashes; the key is the sha256
// of the raw infohash bytes.
//
// Observations come from work the operator was doing anyway: adding a
// torrent to the service reveals instantly whether it was cached.
package debrid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sirrobot01/hearsay"
)

const (
	RealDebrid = "realdebrid"
	AllDebrid  = "alldebrid"
	TorBox     = "torbox"
	Premiumize = "premiumize"
)

// Providers lists the debrid services shipped with v1.
var Providers = []string{RealDebrid, AllDebrid, TorBox, Premiumize}

type Domain struct {
	provider string
}

func New(provider string) Domain {
	return Domain{provider: provider}
}

func (d Domain) Namespace() string {
	return "debrid." + d.provider + ".cached"
}

// Encode accepts a hex infohash string (v1 or v2) or raw [20]byte.
func (d Domain) Encode(subject any) (hearsay.Key, error) {
	switch s := subject.(type) {
	case string:
		raw, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s)))
		if err != nil || (len(raw) != 20 && len(raw) != 32) {
			return hearsay.Key{}, fmt.Errorf("debrid: not an infohash: %q", s)
		}
		return sha256.Sum256(raw), nil
	case [20]byte:
		return sha256.Sum256(s[:]), nil
	}
	return hearsay.Key{}, fmt.Errorf("debrid: unsupported subject type %T", subject)
}

func (d Domain) ValueType() hearsay.ValueType {
	return hearsay.Bool
}

// Decay halves confidence every 24 hours: debrid caches churn on a
// scale of hours to days.
func (d Domain) Decay(age time.Duration) float64 {
	return math.Exp2(-age.Hours() / 24)
}

// Verify interprets the result of actually adding the subject to the
// provider: true means the add was served instantly from cache.
// Anything else reveals nothing.
func (d Domain) Verify(subject any, outcome any) hearsay.Truth {
	if instant, ok := outcome.(bool); ok {
		v := 0.0
		if instant {
			v = 1
		}
		return hearsay.Truth{Known: true, Value: v}
	}
	return hearsay.Truth{}
}
