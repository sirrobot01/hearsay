// Package usenet provides Hearsay domains for article completeness by
// backbone. Namespaces are keyed by backbone rather than reseller,
// because completeness is a property of the backbone.
//
// Subjects are content identifiers: the lowercase hex sha256 of the
// NZB file. The key is the sha256 of those raw hash bytes. Every
// downloader learns completeness for every NZB they touch; that is the
// observation.
package usenet

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
	Omicron       = "omicron"
	UsenetExpress = "usenetexpress"
	Abavia        = "abavia"
)

// Backbones lists the backbones shipped with v1.
var Backbones = []string{Omicron, UsenetExpress, Abavia}

type Domain struct {
	backbone string
}

func New(backbone string) Domain {
	return Domain{backbone: backbone}
}

func (d Domain) Namespace() string {
	return "usenet." + d.backbone + ".complete"
}

// Encode accepts the lowercase hex sha256 of the NZB file, or the raw
// [32]byte digest.
func (d Domain) Encode(subject any) (hearsay.Key, error) {
	switch s := subject.(type) {
	case string:
		raw, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s)))
		if err != nil || len(raw) != 32 {
			return hearsay.Key{}, fmt.Errorf("usenet: not a sha256 content id: %q", s)
		}
		return sha256.Sum256(raw), nil
	case [32]byte:
		return sha256.Sum256(s[:]), nil
	}
	return hearsay.Key{}, fmt.Errorf("usenet: unsupported subject type %T", subject)
}

func (d Domain) ValueType() hearsay.ValueType {
	return hearsay.Scalar
}

// Decay halves confidence every 60 days: completeness declines over
// months as takedowns land, monotonically.
func (d Domain) Decay(age time.Duration) float64 {
	return math.Exp2(-age.Hours() / (60 * 24))
}

// Verify interprets a download attempt. A float64 is the completed
// article fraction after par2 repair; a bool is full success or
// failure. Anything else reveals nothing.
func (d Domain) Verify(subject any, outcome any) hearsay.Truth {
	switch o := outcome.(type) {
	case float64:
		return hearsay.Truth{Known: true, Value: min(max(o, 0), 1)}
	case bool:
		v := 0.0
		if o {
			v = 1
		}
		return hearsay.Truth{Known: true, Value: v}
	}
	return hearsay.Truth{}
}
