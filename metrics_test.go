package hearsay

import (
	"reflect"
	"testing"
	"time"
)

func TestEngineMetricsPersist(t *testing.T) {
	dir := t.TempDir()
	domain := testDomain{"ns.bool", Bool}
	h, err := New(dir, domain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Query(domain.Namespace(), "missing"); err != nil {
		t.Fatal(err)
	}
	if err := h.Observe(domain.Namespace(), "known", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Query(domain.Namespace(), "known"); err != nil {
		t.Fatal(err)
	}
	if err := h.Report(domain.Namespace(), "known", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Publish(domain.Namespace()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Ingest([]byte("invalid")); err == nil {
		t.Fatal("invalid generation accepted")
	}

	metrics := h.Metrics()
	if metrics.Observations != 2 || metrics.Outcomes != 1 || metrics.Queries != 2 ||
		metrics.Known != 1 || metrics.Unknown != 1 || metrics.Local != 1 ||
		metrics.Published != 1 || metrics.RejectedIngests != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if got := metrics.Namespaces[domain.Namespace()]; got.Queries != 2 || got.Observations != 2 {
		t.Fatalf("namespace metrics = %+v", got)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dir, domain)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Metrics(); !reflect.DeepEqual(got, metrics) {
		t.Fatalf("persisted metrics = %+v, want %+v", got, metrics)
	}
}

func TestMetricsRates(t *testing.T) {
	metrics := MetricsCounters{Queries: 4, Known: 3, VerifiedClaims: 5, Agreements: 4}
	if metrics.KnownRate() != 0.75 || metrics.SourceAccuracy() != 0.8 {
		t.Fatalf("rates = %v, %v", metrics.KnownRate(), metrics.SourceAccuracy())
	}
}
