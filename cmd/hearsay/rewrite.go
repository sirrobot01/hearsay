package main

import (
	"bytes"
	"encoding/xml"
	"sort"
	"strconv"
)

type claim struct {
	Subject    string  `json:"subject"`
	Confidence float64 `json:"confidence"`
	Sources    int     `json:"sources"`
	AgeSeconds int64   `json:"age_seconds"`
	Coverage   float64 `json:"coverage"`
}

// cached reports whether any source positively claims the subject.
// Bool payloads are bloom filters, so a hit is evidence and a miss is
// only absence of evidence, never a denial: one source claiming a
// release is the strongest signal that exists for it. The daemon's
// own value field collapses confidence at 0.5, which discards that
// signal whenever a namespace holds more publishers than happen to
// agree — against two equally fresh feeds a lone claiming source
// scores 0.49 and the release sinks. Confidence orders what promotes,
// so thin claims still rank below corroborated ones.
func (c claim) cached() bool {
	return c.Sources > 0
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

// rewrite annotates each release with hearsay attributes and reorders
// them: claimed-cached first by confidence, everything else in
// upstream order. On any failure the body passes through untouched —
// the proxy must never break the pipeline.
func rewrite(body []byte, lookup func([]string) (map[string]claim, error)) []byte {
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
		if a, ok := answers[it.infohash]; ok && a.cached() {
			return a.Confidence
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
				strconv.FormatBool(a.cached()) + `" />`)
		}
		out.Write(block[closeIdx:])
	}
	out.Write(body[last:])
	return out.Bytes()
}
