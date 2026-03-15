package inprocess

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
)

func TestMetrics_RecordAndSnapshot(t *testing.T) {
	m := NewMetrics()

	m.Record("tool_a", 10*time.Millisecond, false, 3)
	m.Record("tool_a", 20*time.Millisecond, false, 5)
	m.Record("tool_a", 100*time.Millisecond, true, 2)

	stats := m.Snapshot()
	if len(stats) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(stats))
	}

	s := stats[0]
	if s.Name != "tool_a" {
		t.Errorf("Name = %q, want %q", s.Name, "tool_a")
	}
	if s.Calls != 3 {
		t.Errorf("Calls = %d, want 3", s.Calls)
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1", s.Errors)
	}
	// Error rate = 1/3 ≈ 0.3333
	if s.ErrorRate < 0.33 || s.ErrorRate > 0.34 {
		t.Errorf("ErrorRate = %f, want ~0.3333", s.ErrorRate)
	}
}

func TestMetrics_MultipleTools(t *testing.T) {
	m := NewMetrics()

	m.Record("tool_x", 5*time.Millisecond, false, 1)
	m.Record("tool_y", 15*time.Millisecond, false, 2)
	m.Record("tool_x", 10*time.Millisecond, false, 1)

	stats := m.Snapshot()
	if len(stats) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(stats))
	}

	// Sorted by calls descending — tool_x has 2 calls, tool_y has 1.
	if stats[0].Name != "tool_x" {
		t.Errorf("first tool should be tool_x (most calls), got %q", stats[0].Name)
	}
	if stats[0].Calls != 2 {
		t.Errorf("tool_x calls = %d, want 2", stats[0].Calls)
	}
	if stats[1].Calls != 1 {
		t.Errorf("tool_y calls = %d, want 1", stats[1].Calls)
	}
}

func TestMetrics_Percentiles(t *testing.T) {
	m := NewMetrics()

	// Record 100 calls with latencies 1ms, 2ms, ..., 100ms.
	for i := 1; i <= 100; i++ {
		m.Record("tool", time.Duration(i)*time.Millisecond, false, 0)
	}

	stats := m.Snapshot()
	if len(stats) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(stats))
	}

	s := stats[0]
	// P50 should be ~50ms.
	if s.P50Ms < 49 || s.P50Ms > 51 {
		t.Errorf("P50 = %.1f, want ~50", s.P50Ms)
	}
	// P95 should be ~95ms.
	if s.P95Ms < 94 || s.P95Ms > 96 {
		t.Errorf("P95 = %.1f, want ~95", s.P95Ms)
	}
	// P99 should be ~99ms.
	if s.P99Ms < 98 || s.P99Ms > 100 {
		t.Errorf("P99 = %.1f, want ~99", s.P99Ms)
	}
}

func TestMetrics_EmptySnapshot(t *testing.T) {
	m := NewMetrics()
	stats := m.Snapshot()
	if len(stats) != 0 {
		t.Errorf("expected 0 tools, got %d", len(stats))
	}
}

func TestMetrics_ZeroErrors(t *testing.T) {
	m := NewMetrics()
	m.Record("clean_tool", 5*time.Millisecond, false, 1)

	stats := m.Snapshot()
	if stats[0].ErrorRate != 0 {
		t.Errorf("ErrorRate = %f, want 0", stats[0].ErrorRate)
	}
}

func TestMetrics_AllErrors(t *testing.T) {
	m := NewMetrics()
	m.Record("broken_tool", 5*time.Millisecond, true, 1)
	m.Record("broken_tool", 10*time.Millisecond, true, 1)

	stats := m.Snapshot()
	if stats[0].ErrorRate != 1.0 {
		t.Errorf("ErrorRate = %f, want 1.0", stats[0].ErrorRate)
	}
}

func TestPercentile_EmptySlice(t *testing.T) {
	result := percentile(nil, 0.5)
	if result != 0 {
		t.Errorf("percentile of empty = %f, want 0", result)
	}
}

func TestPercentile_SingleValue(t *testing.T) {
	result := percentile([]float64{42}, 0.95)
	if result != 42 {
		t.Errorf("percentile of single value = %f, want 42", result)
	}
}

func TestRouterHealthCheck_Healthy(t *testing.T) {
	r := NewRouter()
	// Register a dummy tool handler.
	r.toolHandlers["test_tool"] = nil
	r.toolDefs["test_tool"] = nil
	r.storageHandler = &mockStorageHandler{}

	health := r.HealthCheck()
	if health["status"] != "healthy" {
		t.Errorf("status = %v, want healthy", health["status"])
	}
	if health["tools_registered"].(int) != 1 {
		t.Errorf("tools_registered = %v, want 1", health["tools_registered"])
	}
	if health["has_storage"].(bool) != true {
		t.Errorf("has_storage = %v, want true", health["has_storage"])
	}
}

func TestRouterHealthCheck_Degraded_NoStorage(t *testing.T) {
	r := NewRouter()
	r.toolDefs["test_tool"] = nil

	health := r.HealthCheck()
	if health["status"] != "degraded" {
		t.Errorf("status = %v, want degraded (no storage)", health["status"])
	}
}

func TestRouterHealthCheck_Degraded_NoTools(t *testing.T) {
	r := NewRouter()

	health := r.HealthCheck()
	if health["status"] != "degraded" {
		t.Errorf("status = %v, want degraded (no tools)", health["status"])
	}
}

func TestRouterGetMetrics(t *testing.T) {
	r := NewRouter()
	if r.GetMetrics() == nil {
		t.Error("GetMetrics() should not return nil")
	}
}

// mockStorageHandler satisfies the StorageHandler interface for health check tests.
type mockStorageHandler struct{}

func (m *mockStorageHandler) Read(_ context.Context, _ *pluginv1.StorageReadRequest) (*pluginv1.StorageReadResponse, error) {
	return nil, nil
}
func (m *mockStorageHandler) Write(_ context.Context, _ *pluginv1.StorageWriteRequest) (*pluginv1.StorageWriteResponse, error) {
	return nil, nil
}
func (m *mockStorageHandler) Delete(_ context.Context, _ *pluginv1.StorageDeleteRequest) (*pluginv1.StorageDeleteResponse, error) {
	return nil, nil
}
func (m *mockStorageHandler) List(_ context.Context, _ *pluginv1.StorageListRequest) (*pluginv1.StorageListResponse, error) {
	return nil, nil
}
