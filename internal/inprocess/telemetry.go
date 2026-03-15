package inprocess

import (
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// Metrics collects per-tool call metrics: count, errors, and latency percentiles.
// Thread-safe for concurrent use from the router.
type Metrics struct {
	mu    sync.Mutex
	tools map[string]*toolMetrics
}

type toolMetrics struct {
	calls    int64
	errors   int64
	latencyMs []float64
}

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		tools: make(map[string]*toolMetrics),
	}
}

// Record records a tool call with its duration, error status, and input size.
func (m *Metrics) Record(toolName string, duration time.Duration, isError bool, inputSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tm, ok := m.tools[toolName]
	if !ok {
		tm = &toolMetrics{}
		m.tools[toolName] = tm
	}

	tm.calls++
	if isError {
		tm.errors++
	}
	tm.latencyMs = append(tm.latencyMs, float64(duration.Milliseconds()))

	// Cap stored latencies at 10000 entries per tool to bound memory.
	if len(tm.latencyMs) > 10000 {
		tm.latencyMs = tm.latencyMs[len(tm.latencyMs)-10000:]
	}

	slog.Debug("tool call recorded",
		"tool", toolName,
		"duration_ms", duration.Milliseconds(),
		"is_error", isError,
		"input_size", inputSize,
	)
}

// ToolStats holds aggregated statistics for a single tool.
type ToolStats struct {
	Name      string  `json:"name"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// Snapshot returns current metrics for all tools.
func (m *Metrics) Snapshot() []ToolStats {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := make([]ToolStats, 0, len(m.tools))
	for name, tm := range m.tools {
		var errRate float64
		if tm.calls > 0 {
			errRate = float64(tm.errors) / float64(tm.calls)
		}
		stats = append(stats, ToolStats{
			Name:      name,
			Calls:     tm.calls,
			Errors:    tm.errors,
			ErrorRate: math.Round(errRate*10000) / 10000,
			P50Ms:     percentile(tm.latencyMs, 0.50),
			P95Ms:     percentile(tm.latencyMs, 0.95),
			P99Ms:     percentile(tm.latencyMs, 0.99),
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Calls > stats[j].Calls })
	return stats
}

// percentile calculates the p-th percentile from a slice of values.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	idx := p * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper || upper >= len(sorted) {
		return math.Round(sorted[lower]*100) / 100
	}
	frac := idx - float64(lower)
	return math.Round((sorted[lower]*(1-frac)+sorted[upper]*frac)*100) / 100
}
