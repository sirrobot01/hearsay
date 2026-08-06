package usenet

import (
	"strings"
	"testing"
	"time"
)

func TestEncode(t *testing.T) {
	d := New(Omicron)
	if d.Namespace() != "usenet.omicron.complete" {
		t.Fatal(d.Namespace())
	}
	id := strings.Repeat("ab", 32)
	k1, err := d.Encode(id)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := d.Encode(strings.ToUpper(id))
	if k1 != k2 {
		t.Fatal("encoding is not canonical across case")
	}
	if _, err := d.Encode("abcd"); err == nil {
		t.Fatal("short id accepted")
	}
}

func TestInferBackbone(t *testing.T) {
	cases := map[string]string{
		"news.newshosting.com":   Omicron,
		"NEWS.EWEKA.NL":          Abavia,
		"news.usenetexpress.com": UsenetExpress,
		"news.giganews.com":      Giganews,
		"news.supernews.com":     Giganews,
		"news.newshosting.com:563": Omicron,
		"news.blocknews.net":     "blocknews-net",
		"reader.xsnews.nl":       "xsnews-nl",
		"":                       "",
	}
	for host, want := range cases {
		if got := InferBackbone(host); got != want {
			t.Errorf("InferBackbone(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestVerifyAndDecay(t *testing.T) {
	d := New(Abavia)
	if tr := d.Verify("x", 0.93); !tr.Known || tr.Value != 0.93 {
		t.Fatalf("%+v", tr)
	}
	if tr := d.Verify("x", 1.7); tr.Value != 1 {
		t.Fatal("value not clamped")
	}
	if tr := d.Verify("x", true); !tr.Known || tr.Value != 1 {
		t.Fatalf("%+v", tr)
	}
	if tr := d.Verify("x", nil); tr.Known {
		t.Fatal("nil outcome treated as truth")
	}
	if got := d.Decay(60 * 24 * time.Hour); got < 0.49 || got > 0.51 {
		t.Fatalf("half-life off: %v", got)
	}
	if d.Decay(time.Hour) <= d.Decay(100*24*time.Hour) {
		t.Fatal("decay not monotonic")
	}
}
