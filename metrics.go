package hearsay

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"sync"
)

const metricsFlushEvery = 100

// MetricsCounters describe locally observed engine activity.
type MetricsCounters struct {
	Observations    uint64 `json:"observations"`
	Outcomes        uint64 `json:"outcomes"`
	Queries         uint64 `json:"queries"`
	Known           uint64 `json:"known"`
	Unknown         uint64 `json:"unknown"`
	Local           uint64 `json:"local"`
	Published       uint64 `json:"published"`
	Ingested        uint64 `json:"ingested"`
	RejectedIngests uint64 `json:"rejected_ingests"`
	VerifiedClaims  uint64 `json:"verified_claims"`
	Agreements      uint64 `json:"agreements"`
	FeedsPruned     uint64 `json:"feeds_pruned"`
}

// KnownRate returns the share of valid queries that found evidence.
func (m MetricsCounters) KnownRate() float64 {
	if m.Queries == 0 {
		return 0
	}
	return float64(m.Known) / float64(m.Queries)
}

// SourceAccuracy returns agreement across source claims verified by outcomes.
func (m MetricsCounters) SourceAccuracy() float64 {
	if m.VerifiedClaims == 0 {
		return 0
	}
	return float64(m.Agreements) / float64(m.VerifiedClaims)
}

// EngineMetrics contains aggregate and per-namespace counters.
type EngineMetrics struct {
	MetricsCounters
	Namespaces map[string]MetricsCounters `json:"namespaces"`
}

type metricsLedger struct {
	mu      sync.Mutex
	path    string
	metrics EngineMetrics
	dirty   uint64
}

func openMetricsLedger(path string) (*metricsLedger, error) {
	ledger := &metricsLedger{
		path: path,
		metrics: EngineMetrics{
			Namespaces: map[string]MetricsCounters{},
		},
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &ledger.metrics); err != nil {
		return nil, err
	}
	if ledger.metrics.Namespaces == nil {
		ledger.metrics.Namespaces = map[string]MetricsCounters{}
	}
	return ledger, nil
}

func (m *metricsLedger) record(ns string, update func(*MetricsCounters)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&m.metrics.MetricsCounters)
	if ns != "" {
		counters := m.metrics.Namespaces[ns]
		update(&counters)
		m.metrics.Namespaces[ns] = counters
	}
	m.dirty++
	if m.dirty >= metricsFlushEvery {
		_ = m.saveLocked()
	}
}

func (m *metricsLedger) recordQuery(ns string, answer Answer) {
	m.record(ns, func(counters *MetricsCounters) {
		counters.Queries++
		if answer.Known {
			counters.Known++
		} else {
			counters.Unknown++
		}
		if answer.Local {
			counters.Local++
		}
	})
}

func (m *metricsLedger) recordObservation(ns string) {
	m.record(ns, func(counters *MetricsCounters) { counters.Observations++ })
}

func (m *metricsLedger) recordOutcome(ns string, claims, agreements int) {
	m.record(ns, func(counters *MetricsCounters) {
		counters.Outcomes++
		counters.VerifiedClaims += uint64(claims)
		counters.Agreements += uint64(agreements)
	})
}

func (m *metricsLedger) recordPublished(ns string) {
	m.record(ns, func(counters *MetricsCounters) { counters.Published++ })
}

func (m *metricsLedger) recordIngest(ns string, accepted bool) {
	m.record(ns, func(counters *MetricsCounters) {
		if accepted {
			counters.Ingested++
		} else {
			counters.RejectedIngests++
		}
	})
}

func (m *metricsLedger) recordFeedsPruned(ns string, count int) {
	if count == 0 {
		return
	}
	m.record(ns, func(counters *MetricsCounters) { counters.FeedsPruned += uint64(count) })
}

func (m *metricsLedger) snapshot() EngineMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	metrics := m.metrics
	metrics.Namespaces = maps.Clone(m.metrics.Namespaces)
	return metrics
}

func (m *metricsLedger) saveLocked() error {
	if m.dirty == 0 {
		return nil
	}
	raw, err := json.MarshalIndent(m.metrics, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path+".tmp", raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(m.path+".tmp", m.path); err != nil {
		return err
	}
	m.dirty = 0
	return nil
}

func (m *metricsLedger) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}
