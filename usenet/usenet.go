// Package usenet provides Hearsay domains for article completeness by
// backbone. Namespaces are keyed by backbone rather than reseller,
// because completeness is a property of the backbone.
//
// Subjects are content identifiers: the lowercase hex sha256 over the
// message-ids of every article in the post, sorted byte-wise ascending
// and NUL-separated, each taken as it appears in the NZB without angle
// brackets. Exclude files whose name ends in ".par2"; include every
// other file, so no implementation has to reproduce another's idea of
// which files are incidental. The key is the sha256 of those raw hash
// bytes.
//
// Hashing the NZB file itself would be simpler, but it is not stable.
// The same post re-indexed elsewhere differs in metadata, file order,
// and whitespace, so operators using different indexers would never
// derive the same subject and could never corroborate one another.
// Message-ids are the articles, so they identify the post rather than
// one indexer's description of it, and two separate posts of the same
// content stay distinct — which is correct, because their availability
// genuinely differs.
//
// Every downloader learns completeness for every NZB they touch; that
// is the observation.
package usenet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/sirrobot01/hearsay"
)

const (
	Omicron       = "omicron"
	UsenetExpress = "usenetexpress"
	Abavia        = "abavia"
	Giganews      = "giganews"
)

// Backbones lists the backbones shipped with v1.
var Backbones = []string{Omicron, UsenetExpress, Abavia, Giganews}

// resellers maps well-known NNTP host domains to their backbone.
// Part of the protocol: every implementation must ship the same table
// so independently derived namespaces agree. Only stable,
// high-confidence pairs belong here; explicit operator configuration
// always wins over inference.
var resellers = map[string]string{
	// Omicron Media (Highwinds network)
	"newshosting.com":  Omicron,
	"usenetserver.com": Omicron,
	"easynews.com":     Omicron,
	"tweaknews.eu":     Omicron,

	// UsenetExpress
	"usenetexpress.com": UsenetExpress,
	"newsdemon.com":     UsenetExpress,

	// Abavia
	"eweka.nl":       Abavia,
	"pureusenet.nl":  Abavia,
	"sunnyusenet.com": Abavia,

	// Giganews
	"giganews.com":  Giganews,
	"supernews.com": Giganews,
}

// InferBackbone derives a backbone identifier from an NNTP host when
// the operator did not configure one. Known reseller hosts map to
// their backbone. Unknown hosts fall back to their registrable domain
// with dots replaced by dashes: that silos observations per reseller
// instead of aggregating them per backbone, but it never attributes
// them to the wrong backbone.
func InferBackbone(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return ""
	}
	for domain, backbone := range resellers {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return backbone
		}
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(base, ".", "-")
}

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
