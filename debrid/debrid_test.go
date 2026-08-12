package debrid

import (
	"testing"
	"time"

	"github.com/sirrobot01/hearsay"
)

func TestEncode(t *testing.T) {
	d := New(RealDebrid)
	if d.Namespace() != "debrid.realdebrid.cached" {
		t.Fatal(d.Namespace())
	}
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	k1, err := d.Encode(ih)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := d.Encode("  2C6B6858D61DA9543D4231A71DB4B1C9264B0685 ")
	if k1 != k2 {
		t.Fatal("encoding is not canonical across case and whitespace")
	}
	if _, err := d.Encode("not hex"); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := d.Encode(42); err == nil {
		t.Fatal("unsupported type accepted")
	}
}

func TestUncached(t *testing.T) {
	d := NewUncached(RealDebrid)
	if d.Namespace() != "debrid.realdebrid.uncached" {
		t.Fatal(d.Namespace())
	}
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	k1, err := d.Encode(ih)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := New(RealDebrid).Encode(ih)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Fatal("paired namespaces must encode the same subject to the same key")
	}

	// An instant add proves the denial wrong.
	if tr := d.Verify(ih, true); !tr.Known || tr.Value != 0 {
		t.Fatalf("instant add: %+v", tr)
	}
	if tr := d.Verify(ih, false); !tr.Known || tr.Value != 1 {
		t.Fatalf("failed add: %+v", tr)
	}
	if tr := d.Verify(ih, "junk"); tr.Known {
		t.Fatal("junk outcome must reveal nothing")
	}

	// Denials rot four times as fast as cached claims.
	if got := d.Decay(6 * time.Hour); got < 0.49 || got > 0.51 {
		t.Fatalf("decay at half-life = %f", got)
	}
	if d.Decay(6*time.Hour) >= New(RealDebrid).Decay(6*time.Hour) {
		t.Fatal("denials must decay faster than cached claims")
	}
}

func TestVerifyAndDecay(t *testing.T) {
	d := New(TorBox)
	if tr := d.Verify("x", true); !tr.Known || tr.Value != 1 {
		t.Fatalf("%+v", tr)
	}
	if tr := d.Verify("x", false); !tr.Known || tr.Value != 0 {
		t.Fatalf("%+v", tr)
	}
	if tr := d.Verify("x", "weird"); tr.Known {
		t.Fatal("unknown outcome treated as truth")
	}
	if got := d.Decay(24 * time.Hour); got < 0.49 || got > 0.51 {
		t.Fatalf("half-life off: %v", got)
	}
	if d.ValueType() != hearsay.Bool {
		t.Fatal("wrong value type")
	}
}
