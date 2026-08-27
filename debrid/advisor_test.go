package debrid

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirrobot01/hearsay"
)

const advisorSubject = "2c6b6858d61da9543d4231a71db4b1c9264b0685"

func advisorEngine(t *testing.T) *hearsay.Hearsay {
	t.Helper()
	engine, err := hearsay.New(t.TempDir(), New(RealDebrid), NewUncached(RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestAdvisorShadowLoop(t *testing.T) {
	engine := advisorEngine(t)
	if err := engine.Observe(New(RealDebrid).Namespace(), advisorSubject, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Observe(NewUncached(RealDebrid).Namespace(), advisorSubject, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	advisor, err := NewAdvisor(engine, RealDebrid, Shadow, path)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := advisor.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != hearsay.EvidencePositive || decision.UseHint {
		t.Fatalf("shadow decision = %+v", decision)
	}
	if err := advisor.RecordOutcome(decision.ID, true); err != nil {
		t.Fatal(err)
	}
	metrics := advisor.Snapshot()
	if metrics.Evaluations != 1 || metrics.Positive != 1 || metrics.Outcomes != 1 || metrics.Correct != 1 || metrics.Accuracy() != 1 || metrics.Coverage() != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}

	reopened, err := NewAdvisor(engine, RealDebrid, Shadow, path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot() != metrics {
		t.Fatalf("persisted metrics = %+v, want %+v", reopened.Snapshot(), metrics)
	}
	if err := advisor.RecordOutcome(decision.ID, true); !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("duplicate outcome = %v", err)
	}
	discarded, err := advisor.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	advisor.Discard(discarded.ID)
	if err := advisor.RecordOutcome(discarded.ID, true); !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("discarded outcome = %v", err)
	}
}

func TestAdvisorOutcomeCorrectsEvidence(t *testing.T) {
	engine := advisorEngine(t)
	if err := engine.Observe(New(RealDebrid).Namespace(), advisorSubject, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Observe(NewUncached(RealDebrid).Namespace(), advisorSubject, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	advisor, err := NewAdvisor(engine, RealDebrid, Active, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := advisor.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.UseHint {
		t.Fatal("active advisor did not apply known evidence")
	}
	if err := advisor.RecordOutcome(decision.ID, false); err != nil {
		t.Fatal(err)
	}
	paired, err := engine.QueryPair(New(RealDebrid).Namespace(), NewUncached(RealDebrid).Namespace(), advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if paired.State != hearsay.EvidenceNegative {
		t.Fatalf("outcome did not correct evidence: %+v", paired)
	}
	metrics := advisor.Snapshot()
	if metrics.HintsUsed != 1 || metrics.Incorrect != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestAdvisorMeasuresAvoidableFailureInShadowMode(t *testing.T) {
	engine := advisorEngine(t)
	if err := engine.Observe(New(RealDebrid).Namespace(), advisorSubject, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Observe(NewUncached(RealDebrid).Namespace(), advisorSubject, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	advisor, err := NewAdvisor(engine, RealDebrid, Shadow, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := advisor.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if err := advisor.RecordOutcome(decision.ID, false); err != nil {
		t.Fatal(err)
	}
	metrics := advisor.Snapshot()
	if metrics.PotentialFailuresAvoided != 1 || metrics.Correct != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

type fixedEvidence struct {
	answer hearsay.Answer
}

func (f fixedEvidence) QueryPair(string, string, any) (hearsay.PairedAnswer, error) {
	return hearsay.PairedAnswer{State: hearsay.EvidencePositive, Positive: f.answer}, nil
}

func (fixedEvidence) Report(string, any, any) error { return nil }

func TestAdvisorRejectsProbationaryEvidence(t *testing.T) {
	engine := fixedEvidence{answer: hearsay.Answer{
		Known: true, Support: 1, EvidenceWeight: 0.25, Sources: 10,
	}}
	advisor, err := NewAdvisor(engine, RealDebrid, Active, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := advisor.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if decision.UseHint {
		t.Fatal("probationary evidence affected active behavior")
	}

	engine.answer.Local = true
	local, err := NewAdvisor(engine, RealDebrid, Active, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err = local.Evaluate(advisorSubject)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.UseHint {
		t.Fatal("local evidence did not affect active behavior")
	}
}
