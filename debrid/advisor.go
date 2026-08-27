package debrid

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirrobot01/hearsay"
)

type AdviceMode uint8

// Advice modes separate measurement from behavior changes.
const (
	Shadow AdviceMode = iota
	Active
)

// EvidenceEngine is the Hearsay surface used by an Advisor.
type EvidenceEngine interface {
	QueryPair(positiveNS, negativeNS string, subject any) (hearsay.PairedAnswer, error)
	Report(ns string, subject any, outcome any) error
}

// Decision captures the evidence seen before an add.
type Decision struct {
	ID       string
	Subject  string
	State    hearsay.EvidenceState
	UseHint  bool
	Positive hearsay.Answer
	Negative hearsay.Answer
}

// AdvicePolicy controls which remote evidence can affect host behavior.
type AdvicePolicy struct {
	MinSupport  float64
	MinEvidence float64
	MinSources  int
}

// DefaultAdvicePolicy rejects evidence that fits inside the unverified pool.
func DefaultAdvicePolicy() AdvicePolicy {
	return AdvicePolicy{MinSupport: 0.5, MinEvidence: 0.3, MinSources: 1}
}

// AdvisorMetrics measures coverage and verified prediction quality locally.
type AdvisorMetrics struct {
	Evaluations              uint64 `json:"evaluations"`
	Positive                 uint64 `json:"positive"`
	Negative                 uint64 `json:"negative"`
	Unknown                  uint64 `json:"unknown"`
	HintsUsed                uint64 `json:"hints_used"`
	Outcomes                 uint64 `json:"outcomes"`
	Verified                 uint64 `json:"verified"`
	Correct                  uint64 `json:"correct"`
	Incorrect                uint64 `json:"incorrect"`
	PotentialFailuresAvoided uint64 `json:"potential_failures_avoided"`
}

// Accuracy returns the verified correct-outcome ratio.
func (m AdvisorMetrics) Accuracy() float64 {
	if m.Verified == 0 {
		return 0
	}
	return float64(m.Correct) / float64(m.Verified)
}

// Coverage returns the share of evaluations with positive or negative evidence.
func (m AdvisorMetrics) Coverage() float64 {
	if m.Evaluations == 0 {
		return 0
	}
	return float64(m.Positive+m.Negative) / float64(m.Evaluations)
}

// ErrUnknownDecision means an outcome was duplicated, discarded, or never evaluated.
var ErrUnknownDecision = errors.New("debrid: unknown decision")

// Advisor closes the query, action, outcome, and learning loop for debrid adds.
type Advisor struct {
	engine     EvidenceEngine
	positiveNS string
	negativeNS string
	mode       AdviceMode
	policy     AdvicePolicy
	path       string

	mu      sync.Mutex
	metrics AdvisorMetrics
	pending map[string]Decision
}

// NewAdvisor creates a provider-specific advisor. An empty metrics path is in-memory only.
func NewAdvisor(engine EvidenceEngine, provider string, mode AdviceMode, metricsPath string) (*Advisor, error) {
	return NewAdvisorWithPolicy(engine, provider, mode, DefaultAdvicePolicy(), metricsPath)
}

// NewAdvisorWithPolicy creates an advisor with explicit activation thresholds.
func NewAdvisorWithPolicy(engine EvidenceEngine, provider string, mode AdviceMode, policy AdvicePolicy, metricsPath string) (*Advisor, error) {
	if engine == nil {
		return nil, fmt.Errorf("debrid: advisor engine is required")
	}
	if mode != Shadow && mode != Active {
		return nil, fmt.Errorf("debrid: invalid advice mode")
	}
	if !validProvider(provider) {
		return nil, fmt.Errorf("debrid: invalid provider %q", provider)
	}
	if policy.MinSupport < 0 || policy.MinSupport > 1 || policy.MinEvidence < 0 || policy.MinSources < 1 {
		return nil, fmt.Errorf("debrid: invalid advice policy")
	}
	advisor := &Advisor{
		engine:     engine,
		positiveNS: New(provider).Namespace(),
		negativeNS: NewUncached(provider).Namespace(),
		mode:       mode,
		policy:     policy,
		path:       metricsPath,
		pending:    map[string]Decision{},
	}
	if err := advisor.load(); err != nil {
		return nil, err
	}
	return advisor, nil
}

// Evaluate records one pre-add prediction.
func (a *Advisor) Evaluate(subject string) (Decision, error) {
	decisions, err := a.EvaluateBatch([]string{subject})
	if len(decisions) == 0 {
		return Decision{}, err
	}
	return decisions[0], err
}

// EvaluateBatch records predictions for a release set with one metrics write.
func (a *Advisor) EvaluateBatch(subjects []string) ([]Decision, error) {
	decisions := make([]Decision, 0, len(subjects))
	for _, subject := range subjects {
		paired, err := a.engine.QueryPair(a.positiveNS, a.negativeNS, subject)
		if err != nil {
			return nil, err
		}
		id, err := decisionID()
		if err != nil {
			return nil, err
		}
		evidence := paired.Positive
		if paired.State == hearsay.EvidenceNegative {
			evidence = paired.Negative
		}
		decisions = append(decisions, Decision{
			ID:       id,
			Subject:  subject,
			State:    paired.State,
			UseHint:  a.mode == Active && a.accepts(evidence),
			Positive: paired.Positive,
			Negative: paired.Negative,
		})
	}

	a.mu.Lock()
	for _, decision := range decisions {
		a.pending[decision.ID] = decision
		a.metrics.Evaluations++
		switch decision.State {
		case hearsay.EvidencePositive:
			a.metrics.Positive++
		case hearsay.EvidenceNegative:
			a.metrics.Negative++
		default:
			a.metrics.Unknown++
		}
		if decision.UseHint {
			a.metrics.HintsUsed++
		}
	}
	err := a.saveLocked()
	a.mu.Unlock()
	return decisions, err
}

func (a *Advisor) accepts(answer hearsay.Answer) bool {
	if !answer.Known {
		return false
	}
	if answer.Local {
		return true
	}
	return answer.Support >= a.policy.MinSupport &&
		answer.EvidenceWeight >= a.policy.MinEvidence &&
		answer.Sources >= a.policy.MinSources
}

// RecordOutcome verifies a decision and reports the add result to both namespaces.
func (a *Advisor) RecordOutcome(decisionID string, instant bool) error {
	a.mu.Lock()
	decision, ok := a.pending[decisionID]
	if ok {
		delete(a.pending, decisionID)
	}
	a.mu.Unlock()
	if !ok {
		return ErrUnknownDecision
	}

	reportErr := errors.Join(
		a.engine.Report(a.positiveNS, decision.Subject, instant),
		a.engine.Report(a.negativeNS, decision.Subject, instant),
	)
	actual := hearsay.EvidenceNegative
	if instant {
		actual = hearsay.EvidencePositive
	}

	a.mu.Lock()
	a.metrics.Outcomes++
	if decision.State != hearsay.EvidenceUnknown {
		a.metrics.Verified++
		if decision.State == actual {
			a.metrics.Correct++
		} else {
			a.metrics.Incorrect++
		}
	}
	if a.mode == Shadow && decision.State == hearsay.EvidenceNegative && !instant {
		a.metrics.PotentialFailuresAvoided++
	}
	saveErr := a.saveLocked()
	a.mu.Unlock()
	return errors.Join(reportErr, saveErr)
}

// Discard drops decisions for candidates the host did not act on.
func (a *Advisor) Discard(decisionIDs ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range decisionIDs {
		delete(a.pending, id)
	}
}

// Snapshot returns a consistent copy of local metrics.
func (a *Advisor) Snapshot() AdvisorMetrics {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.metrics
}

func validProvider(provider string) bool {
	if provider == "" || len(provider) > 43 {
		return false
	}
	for _, char := range provider {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func decisionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (a *Advisor) load() error {
	if a.path == "" {
		return nil
	}
	raw, err := os.ReadFile(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &a.metrics)
}

func (a *Advisor) saveLocked() error {
	if a.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a.metrics, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}
