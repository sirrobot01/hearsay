package main

import (
	"bytes"
	"strings"
	"testing"
)

const fixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<title>indexer</title>
<item>
<title>Release.A.1080p</title>
<guid>a</guid>
<torznab:attr name="infohash" value="aaaa" />
<torznab:attr name="seeders" value="10" />
</item>
<item>
<title>Release.B.1080p</title>
<guid>b</guid>
<torznab:attr name="infohash" value="bbbb" />
</item>
<item>
<title>Release.C.no-hash</title>
<guid>c</guid>
</item>
</channel>
</rss>`

func TestRewrite(t *testing.T) {
	answers := map[string]claim{
		"aaaa": {Subject: "aaaa", Value: false, Confidence: 0.1, Sources: 2, Coverage: 0.5},
		"bbbb": {Subject: "bbbb", Value: true, Confidence: 0.92, Sources: 4, Coverage: 0.8},
	}
	out := string(rewrite([]byte(fixture), func(subjects []string) (map[string]claim, error) {
		if len(subjects) != 2 {
			t.Fatalf("subjects = %v", subjects)
		}
		return answers, nil
	}))

	if !strings.Contains(out, `<title>indexer</title>`) || !strings.Contains(out, `xmlns:torznab`) {
		t.Fatal("channel metadata lost")
	}
	bIdx := strings.Index(out, "Release.B")
	aIdx := strings.Index(out, "Release.A")
	cIdx := strings.Index(out, "Release.C")
	if bIdx == -1 || aIdx == -1 || cIdx == -1 || bIdx > aIdx || aIdx > cIdx {
		t.Fatalf("order wrong: B=%d A=%d C=%d", bIdx, aIdx, cIdx)
	}
	if !strings.Contains(out, `name="hearsay_confidence" value="0.92"`) {
		t.Fatal("confidence attribute missing")
	}
	if !strings.Contains(out, `name="hearsay_cached" value="true"`) {
		t.Fatal("cached attribute missing")
	}
	if strings.Count(out, "<item>") != 3 || strings.Count(out, "</item>") != 3 {
		t.Fatal("item count changed")
	}
}

func TestRewritePassThrough(t *testing.T) {
	noItems := `<?xml version="1.0"?><rss><channel><title>x</title></channel></rss>`
	if got := rewrite([]byte(noItems), nil); !bytes.Equal(got, []byte(noItems)) {
		t.Fatal("empty result modified")
	}
	noHashes := `<rss><channel><item><title>y</title></item></channel></rss>`
	if got := rewrite([]byte(noHashes), nil); !bytes.Equal(got, []byte(noHashes)) {
		t.Fatal("hashless result modified")
	}
	broken := []byte(`<rss><channel><item><torznab:attr name="infohash" value="aa" /></item>`)
	got := rewrite(broken, func([]string) (map[string]claim, error) {
		return map[string]claim{"aa": {Subject: "aa", Value: true, Confidence: 1, Sources: 1}}, nil
	})
	if !strings.Contains(string(got), "hearsay_confidence") {
		t.Fatal("valid prefix of truncated doc not annotated")
	}
}
