// Package torznab annotates and reorders Torznab search results with
// hearsay cache claims, for any embedder that can answer a batch of
// infohash subjects. The hearsay CLI wraps it around the daemon's HTTP
// API; decypharr wraps it around an in-process engine.
//
// Promotion works on the torznab "infohash" attribute, so only torrent
// releases participate. Newznab results carry no message-ids at search
// time, so a usenet subject cannot be derived here.
package torznab

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Claim is one subject's resolved evidence. Its JSON tags match a side
// of the daemon's paired evidence response.
//
// Denied marks positive evidence of absence from a paired namespace.
// A local false Value is equivalent evidence from the daemon.
type Claim struct {
	Subject        string  `json:"subject"`
	Known          bool    `json:"known"`
	Value          *bool   `json:"value,omitempty"`
	Support        float64 `json:"support"`
	EvidenceWeight float64 `json:"evidence_weight"`
	Sources        int     `json:"sources"`
	AgeSeconds     int64   `json:"age_seconds"`
	Coverage       float64 `json:"coverage"`
	Local          bool    `json:"local,omitzero"`
	Denied         bool    `json:"denied,omitzero"`
}

// Cached reports positive evidence without treating a Bloom miss as a denial.
func (c Claim) Cached() bool {
	if c.Denied {
		return false
	}
	if c.Known && c.Value != nil {
		return *c.Value
	}
	return c.Sources > 0 && !c.Denied
}

// Uncached reports explicit negative evidence, including local truth.
func (c Claim) Uncached() bool {
	return c.Denied || c.Known && c.Value != nil && !*c.Value
}

// State returns cached, uncached, or unknown.
func (c Claim) State() string {
	switch {
	case c.Cached():
		return "cached"
	case c.Uncached():
		return "uncached"
	default:
		return "unknown"
	}
}

// RankPolicy controls which evidence may alter upstream order.
type RankPolicy struct {
	MinSupport float64
	MinSources int
	MaxAge     time.Duration
}

// DefaultRankPolicy accepts current evidence with majority support.
func DefaultRankPolicy() RankPolicy {
	return RankPolicy{
		MinSupport: 0.5,
		MinSources: 1,
		MaxAge:     24 * time.Hour,
	}
}

// Accepts reports whether evidence is strong and recent enough to reorder.
func (p RankPolicy) Accepts(c Claim) bool {
	if c.Local && c.Known && c.Value != nil {
		return true
	}
	defaults := DefaultRankPolicy()
	if p == (RankPolicy{}) {
		p = defaults
	} else {
		if p.MinSources == 0 {
			p.MinSources = defaults.MinSources
		}
		if p.MaxAge == 0 {
			p.MaxAge = defaults.MaxAge
		}
	}
	return c.Sources >= p.MinSources &&
		c.Support >= p.MinSupport &&
		(p.MaxAge < 0 || time.Duration(c.AgeSeconds)*time.Second <= p.MaxAge)
}

// Lookup answers a batch of infohash subjects. Missing subjects mean
// "unknown" and keep their upstream position.
//
// Remote Sources count positive claims. Denied and local false values
// carry explicit negative evidence.
type Lookup func(subjects []string) (map[string]Claim, error)

// Proxy forwards every request to Upstream unchanged. On successful
// search results it annotates each release with hearsay attributes and
// moves claimed-cached releases to the top. On any failure the
// upstream result passes through untouched — the proxy must never
// break the pipeline.
type Proxy struct {
	// Upstream is the Torznab base URL, e.g. "http://prowlarr:9696".
	// The request path and query are appended as-is.
	Upstream string
	// Lookup answers the infohash subjects found in a result page.
	Lookup Lookup
	// Client issues upstream requests. Nil means a client with a
	// 2-minute timeout.
	Client *http.Client
	Policy RankPolicy
}

var searchTypes = map[string]bool{
	"search": true, "tvsearch": true, "movie": true, "music": true, "book": true,
}

var defaultClient = &http.Client{Timeout: 2 * time.Minute}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSuffix(p.Upstream, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	// The rewrite needs plain XML, not a gzip stream.
	req.Header.Del("Accept-Encoding")
	client := p.Client
	if client == nil {
		client = defaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK || !searchTypes[r.URL.Query().Get("t")] {
		copyResponse(w, res)
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := RewriteWithPolicy(body, p.Lookup, p.Policy)
	copyHeaders(w, res)
	w.WriteHeader(res.StatusCode)
	w.Write(out)
}

func copyHeaders(w http.ResponseWriter, res *http.Response) {
	for k, vs := range res.Header {
		if k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

func copyResponse(w http.ResponseWriter, res *http.Response) {
	copyHeaders(w, res)
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}

type item struct {
	start, end int64
	infohash   string
}

// splitItems locates each <item> block by raw token offsets, so the
// rest of the document round-trips byte for byte, and extracts the
// torznab infohash attribute per item.
func splitItems(body []byte) []item {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var items []item
	var cur item
	inItem := false
	for {
		pre := dec.InputOffset()
		tok, err := dec.RawToken()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "item" && !inItem:
				inItem = true
				cur = item{start: pre}
			case inItem && t.Name.Local == "attr":
				var name, value string
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "name":
						name = a.Value
					case "value":
						value = a.Value
					}
				}
				if name == "infohash" {
					cur.infohash = value
				}
			}
		case xml.EndElement:
			if inItem && t.Name.Local == "item" {
				cur.end = dec.InputOffset()
				items = append(items, cur)
				inItem = false
			}
		}
	}
	return items
}

// Rewrite annotates releases and reorders evidence accepted by the
// default ranking policy. On failure the body passes through untouched.
func Rewrite(body []byte, lookup Lookup) []byte {
	return RewriteWithPolicy(body, lookup, DefaultRankPolicy())
}

// RewriteWithPolicy applies an explicit ranking policy.
func RewriteWithPolicy(body []byte, lookup Lookup, policy RankPolicy) []byte {
	items := splitItems(body)
	if len(items) == 0 {
		return body
	}
	var subjects []string
	for _, it := range items {
		if it.infohash != "" {
			subjects = append(subjects, it.infohash)
		}
	}
	if len(subjects) == 0 {
		return body
	}
	answers, err := lookup(subjects)
	if err != nil {
		return body
	}

	first, last := items[0].start, items[len(items)-1].end
	rank := func(it item) float64 {
		a, ok := answers[it.infohash]
		if !ok {
			return -1
		}
		if !policy.Accepts(a) {
			return -1
		}
		if a.Cached() {
			return a.Support
		}
		if a.Uncached() {
			return -2 - a.Support
		}
		return -1
	}
	sort.SliceStable(items, func(i, j int) bool {
		return rank(items[i]) > rank(items[j])
	})

	var out bytes.Buffer
	out.Grow(len(body) + 128*len(items))
	out.Write(body[:first])
	for i, it := range items {
		if i > 0 {
			out.WriteByte('\n')
		}
		block := body[it.start:it.end]
		a, ok := answers[it.infohash]
		closeIdx := bytes.LastIndex(block, []byte("</item>"))
		if !ok || closeIdx < 0 {
			out.Write(block)
			continue
		}
		out.Write(block[:closeIdx])
		out.WriteString(`<torznab:attr name="hearsay_support" value="` +
			strconv.FormatFloat(a.Support, 'f', 2, 64) + `" />`)
		out.WriteString(`<torznab:attr name="hearsay_evidence_weight" value="` +
			strconv.FormatFloat(a.EvidenceWeight, 'f', 2, 64) + `" />`)
		out.WriteString(`<torznab:attr name="hearsay_state" value="` + a.State() + `" />`)
		if a.State() != "unknown" {
			out.WriteString(`<torznab:attr name="hearsay_cached" value="` +
				strconv.FormatBool(a.Cached()) + `" />`)
		}
		out.Write(block[closeIdx:])
	}
	out.Write(body[last:])
	return out.Bytes()
}
