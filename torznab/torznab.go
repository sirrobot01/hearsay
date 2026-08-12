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

// Claim is one subject's answer. The JSON tags match the daemon's
// /v1/claims response, so a daemon-backed Lookup can decode into it
// directly.
//
// Denied marks positive evidence of absence: sources claimed the
// subject in a paired denial namespace (added and found not cached).
// A denied claim's Confidence and Sources describe the denial, and
// Rewrite sinks it below every unclaimed release. Daemon answers
// never set it; an engine-backed Lookup may, by pairing namespaces.
type Claim struct {
	Subject    string  `json:"subject"`
	Confidence float64 `json:"confidence"`
	Sources    int     `json:"sources"`
	AgeSeconds int64   `json:"age_seconds"`
	Coverage   float64 `json:"coverage"`
	Denied     bool    `json:"denied,omitempty"`
}

// Cached reports whether any source positively claims the subject.
// Bool payloads are bloom filters, so a hit is evidence and a miss is
// only absence of evidence, never a denial: one source claiming a
// release is the strongest signal that exists for it. The daemon's
// own value field collapses confidence at 0.5, which discards that
// signal whenever a namespace holds more publishers than happen to
// agree — against two equally fresh feeds a lone claiming source
// scores 0.49 and the release sinks. Confidence orders what promotes,
// so thin claims still rank below corroborated ones.
func (c Claim) Cached() bool {
	return c.Sources > 0 && !c.Denied
}

// Lookup answers a batch of infohash subjects. Missing subjects mean
// "unknown" and keep their upstream position.
//
// A Claim must carry positive evidence only: Sources counts sources
// that claim the subject present, never ones that deny it. Network
// answers satisfy this by construction. An engine-backed Lookup must
// take care with local ground truth, which reports Sources: 1 for
// both outcomes — map a local negative to Sources: 0, or the embedder
// promotes releases it has itself proven uncached.
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
	out := Rewrite(body, p.Lookup)
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

// Rewrite annotates each release with hearsay attributes and reorders
// them: claimed-cached first by confidence, everything else in
// upstream order. On any failure the body passes through untouched.
func Rewrite(body []byte, lookup Lookup) []byte {
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
		if a.Cached() {
			return a.Confidence
		}
		// A denial sinks below every unclaimed release; the more
		// confident the denial, the lower.
		if a.Denied {
			return -2 - a.Confidence
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
		out.WriteString(`<torznab:attr name="hearsay_confidence" value="` +
			strconv.FormatFloat(a.Confidence, 'f', 2, 64) + `" />`)
		if a.Sources > 0 || a.Coverage > 0 {
			out.WriteString(`<torznab:attr name="hearsay_cached" value="` +
				strconv.FormatBool(a.Cached()) + `" />`)
		}
		out.Write(block[closeIdx:])
	}
	out.Write(body[last:])
	return out.Bytes()
}
