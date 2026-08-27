package torznab

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	answers := map[string]Claim{
		"aaaa": {Subject: "aaaa", Support: 0.1, EvidenceWeight: 0.1, Sources: 2, Coverage: 0.5},
		"bbbb": {Subject: "bbbb", Support: 0.92, EvidenceWeight: 0.92, Sources: 4, Coverage: 0.8},
	}
	out := string(Rewrite([]byte(fixture), func(subjects []string) (map[string]Claim, error) {
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
	if !strings.Contains(out, `name="hearsay_support" value="0.92"`) {
		t.Fatal("support attribute missing")
	}
	if !strings.Contains(out, `name="hearsay_state" value="cached"`) {
		t.Fatal("state attribute missing")
	}
	if !strings.Contains(out, `name="hearsay_cached" value="true"`) {
		t.Fatal("cached attribute missing")
	}
	if strings.Count(out, "<item>") != 3 || strings.Count(out, "</item>") != 3 {
		t.Fatal("item count changed")
	}
}

func TestRewritePassThrough(t *testing.T) {
	if got := Rewrite([]byte(fixture), nil); !bytes.Equal(got, []byte(fixture)) {
		t.Fatal("nil lookup modified result")
	}
	noItems := `<?xml version="1.0"?><rss><channel><title>x</title></channel></rss>`
	if got := Rewrite([]byte(noItems), nil); !bytes.Equal(got, []byte(noItems)) {
		t.Fatal("empty result modified")
	}
	noHashes := `<rss><channel><item><title>y</title></item></channel></rss>`
	if got := Rewrite([]byte(noHashes), nil); !bytes.Equal(got, []byte(noHashes)) {
		t.Fatal("hashless result modified")
	}
	broken := []byte(`<rss><channel><item><torznab:attr name="infohash" value="aa" /></item>`)
	got := Rewrite(broken, func([]string) (map[string]Claim, error) {
		return map[string]Claim{"aa": {Subject: "aa", Support: 1, EvidenceWeight: 1, Sources: 1}}, nil
	})
	if !strings.Contains(string(got), "hearsay_support") {
		t.Fatal("valid prefix of truncated doc not annotated")
	}
}

func TestRewriteDoesNotPromoteBelowThreshold(t *testing.T) {
	out := string(Rewrite([]byte(fixture), func([]string) (map[string]Claim, error) {
		return map[string]Claim{
			"bbbb": {Subject: "bbbb", Support: 0.49, EvidenceWeight: 1, Sources: 1, Coverage: 1},
		}, nil
	}))
	if !strings.Contains(out, `name="hearsay_cached" value="true"`) {
		t.Error("single-source claim not marked cached")
	}
	bIdx := strings.Index(out, "Release.B")
	aIdx := strings.Index(out, "Release.A")
	if bIdx == -1 || aIdx == -1 || bIdx < aIdx {
		t.Errorf("claim below threshold reordered: B=%d A=%d", bIdx, aIdx)
	}
}

func TestRewriteUsesConfiguredPolicy(t *testing.T) {
	out := string(RewriteWithPolicy([]byte(fixture), func([]string) (map[string]Claim, error) {
		return map[string]Claim{
			"bbbb": {Subject: "bbbb", Support: 0.49, EvidenceWeight: 1, Sources: 1, Coverage: 1},
		}, nil
	}, RankPolicy{MinSupport: 0.4, MinSources: 1, MaxAge: time.Hour}))
	bIdx := strings.Index(out, "Release.B")
	aIdx := strings.Index(out, "Release.A")
	if bIdx == -1 || aIdx == -1 || bIdx > aIdx {
		t.Errorf("configured claim not promoted: B=%d A=%d", bIdx, aIdx)
	}
}

func TestRewriteDemotesLocalNegative(t *testing.T) {
	out := string(Rewrite([]byte(fixture), func([]string) (map[string]Claim, error) {
		return map[string]Claim{
			"aaaa": {Subject: "aaaa", Known: true, Value: new(false), Support: 1, Sources: 1, Local: true},
		}, nil
	}))
	aIdx := strings.Index(out, "Release.A")
	bIdx := strings.Index(out, "Release.B")
	cIdx := strings.Index(out, "Release.C")
	if aIdx == -1 || bIdx == -1 || cIdx == -1 || aIdx < bIdx || aIdx < cIdx {
		t.Errorf("local negative not demoted: A=%d B=%d C=%d", aIdx, bIdx, cIdx)
	}
}

// TestRewriteDemotesDenied checks that a release with positive
// evidence of absence sinks below unclaimed releases, which keep
// their upstream order.
func TestRewriteDemotesDenied(t *testing.T) {
	out := string(Rewrite([]byte(fixture), func([]string) (map[string]Claim, error) {
		return map[string]Claim{
			"aaaa": {Subject: "aaaa", Support: 0.8, EvidenceWeight: 0.8, Sources: 2, Coverage: 1, Denied: true},
		}, nil
	}))
	aIdx := strings.Index(out, "Release.A")
	bIdx := strings.Index(out, "Release.B")
	cIdx := strings.Index(out, "Release.C")
	if aIdx == -1 || bIdx == -1 || cIdx == -1 || aIdx < bIdx || aIdx < cIdx {
		t.Errorf("denied release not demoted: A=%d B=%d C=%d", aIdx, bIdx, cIdx)
	}
	if !strings.Contains(out, `name="hearsay_cached" value="false"`) {
		t.Error("denied release not marked uncached")
	}
}

func TestRewriteMarksUnknown(t *testing.T) {
	out := string(Rewrite([]byte(fixture), func([]string) (map[string]Claim, error) {
		return map[string]Claim{
			"aaaa": {Subject: "aaaa", Support: 0, Sources: 0, Coverage: 1},
		}, nil
	}))
	if !strings.Contains(out, `name="hearsay_state" value="unknown"`) {
		t.Error("unclaimed subject not marked unknown")
	}
	if strings.Contains(out, `name="hearsay_cached" value="false"`) {
		t.Error("unknown subject marked uncached")
	}
}

func TestProxyRewritesSearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1/api" || r.URL.Query().Get("apikey") != "k" {
			t.Errorf("upstream got %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Write([]byte(fixture))
	}))
	defer upstream.Close()

	p := &Proxy{
		Upstream: upstream.URL + "/",
		Lookup: func([]string) (map[string]Claim, error) {
			return map[string]Claim{
				"bbbb": {Subject: "bbbb", Support: 0.9, EvidenceWeight: 0.9, Sources: 3, Coverage: 1},
			}, nil
		},
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/1/api?t=search&apikey=k", nil))

	out := rec.Body.String()
	bIdx := strings.Index(out, "Release.B")
	aIdx := strings.Index(out, "Release.A")
	if bIdx == -1 || aIdx == -1 || bIdx > aIdx {
		t.Errorf("claimed release not promoted: B=%d A=%d", bIdx, aIdx)
	}
	if !strings.Contains(out, "hearsay_support") {
		t.Error("attributes missing")
	}
}

func TestProxyPassesRequestContextToLookup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(fixture))
	}))
	defer upstream.Close()

	type contextKey struct{}
	p := &Proxy{
		Upstream: upstream.URL,
		ContextLookup: func(ctx context.Context, _ []string) (map[string]Claim, error) {
			if ctx.Value(contextKey{}) != "request" {
				t.Fatal("request context was not propagated")
			}
			return map[string]Claim{}, nil
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api?t=search", nil)
	request = request.WithContext(context.WithValue(request.Context(), contextKey{}, "request"))
	p.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestProxyPassesThroughNonSearch(t *testing.T) {
	caps := `<caps><searching/></caps>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(caps))
	}))
	defer upstream.Close()

	p := &Proxy{Upstream: upstream.URL, Lookup: func([]string) (map[string]Claim, error) {
		t.Fatal("lookup called on non-search request")
		return nil, nil
	}}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/1/api?t=caps", nil))
	if rec.Body.String() != caps {
		t.Errorf("caps response modified: %q", rec.Body.String())
	}
}

func TestProxyPassesThroughUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	p := &Proxy{Upstream: upstream.URL, Lookup: func([]string) (map[string]Claim, error) {
		t.Fatal("lookup called on upstream error")
		return nil, nil
	}}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/1/api?t=search", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 passthrough", rec.Code)
	}
	if body, _ := io.ReadAll(rec.Body); !strings.Contains(string(body), "boom") {
		t.Error("error body not passed through")
	}
}
