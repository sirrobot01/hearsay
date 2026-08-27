package swarm

import (
	"strings"
	"testing"
	"time"

	"github.com/sirrobot01/hearsay"
)

func TestDomain(t *testing.T) {
	domain := New()
	v1 := strings.Repeat("ab", 20)
	upper, err := domain.Encode(strings.ToUpper(v1))
	if err != nil {
		t.Fatal(err)
	}
	var raw [20]byte
	for i := range raw {
		raw[i] = 0xab
	}
	encoded, err := domain.Encode(raw)
	if err != nil || encoded != upper {
		t.Fatalf("encoded = %x, %v", encoded, err)
	}
	if _, err := domain.Encode("bad"); err == nil {
		t.Fatal("invalid infohash accepted")
	}
	if domain.Namespace() != Namespace || domain.ValueType() != hearsay.Scalar {
		t.Fatalf("domain = %s, %v", domain.Namespace(), domain.ValueType())
	}
	if domain.Decay(12*time.Hour) < 0.49 || domain.Decay(12*time.Hour) > 0.51 {
		t.Fatalf("decay = %v", domain.Decay(12*time.Hour))
	}
}

func TestVerifyTerminalOutcome(t *testing.T) {
	domain := New()
	for _, test := range []struct {
		outcome any
		known   bool
		value   float64
	}{
		{true, true, 1},
		{false, true, 0},
		{0.42, true, 0.42},
		{2.0, true, 1},
		{"ongoing", false, 0},
	} {
		truth := domain.Verify(nil, test.outcome)
		if truth.Known != test.known || truth.Value != test.value {
			t.Fatalf("Verify(%v) = %+v", test.outcome, truth)
		}
	}
}

func TestEngineWorkflow(t *testing.T) {
	engine, err := hearsay.New(t.TempDir(), New())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	infohash := strings.Repeat("cd", 20)
	if err := engine.Observe(Namespace, infohash, 0.6, time.Now()); err != nil {
		t.Fatal(err)
	}
	answer, err := engine.Query(Namespace, infohash)
	if err != nil || !answer.Known || answer.Value != 0.6 {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
	if err := engine.Report(Namespace, infohash, true); err != nil {
		t.Fatal(err)
	}
	answer, err = engine.Query(Namespace, infohash)
	if err != nil || answer.Value != 1 {
		t.Fatalf("reported answer = %+v, %v", answer, err)
	}
}
