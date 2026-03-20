package inprocess

import (
	"testing"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

func newTestSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return s
}

func registerTestPlugin(r *Router) {
	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID: "tools.features",
		Tools: []plugin.ExportedTool{
			{Name: "create_feature", Description: "Create a new feature", Schema: newTestSchema()},
			{Name: "list_features", Description: "List all features", Schema: newTestSchema()},
			{Name: "advance_feature", Description: "Advance a feature to the next workflow status", Schema: newTestSchema()},
		},
	})
	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID: "tools.marketplace",
		Tools: []plugin.ExportedTool{
			{Name: "install_pack", Description: "Install a pack from the marketplace", Schema: newTestSchema()},
			{Name: "search_packs", Description: "Search for packs in the marketplace", Schema: newTestSchema()},
		},
	})
}

func TestListCatalog_AllTools(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	entries := r.ListCatalog("", 0, 100)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// Should be sorted by name.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Name >= entries[i].Name {
			t.Errorf("entries not sorted: %q >= %q", entries[i-1].Name, entries[i].Name)
		}
	}
}

func TestListCatalog_FilterByPlugin(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	entries := r.ListCatalog("tools.features", 0, 100)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for tools.features, got %d", len(entries))
	}
	for _, e := range entries {
		if e.PluginID != "tools.features" {
			t.Errorf("expected plugin tools.features, got %q", e.PluginID)
		}
	}
}

func TestListCatalog_Pagination(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	// Page 1: offset=0, limit=2
	page1 := r.ListCatalog("", 0, 2)
	if len(page1) != 2 {
		t.Fatalf("expected 2 entries in page 1, got %d", len(page1))
	}

	// Page 2: offset=2, limit=2
	page2 := r.ListCatalog("", 2, 2)
	if len(page2) != 2 {
		t.Fatalf("expected 2 entries in page 2, got %d", len(page2))
	}

	// Page 3: offset=4, limit=2
	page3 := r.ListCatalog("", 4, 2)
	if len(page3) != 1 {
		t.Fatalf("expected 1 entry in page 3, got %d", len(page3))
	}

	// Beyond range.
	page4 := r.ListCatalog("", 10, 2)
	if len(page4) != 0 {
		t.Fatalf("expected 0 entries beyond range, got %d", len(page4))
	}
}

func TestCatalogCount(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	total := r.CatalogCount("")
	if total != 5 {
		t.Fatalf("expected 5, got %d", total)
	}

	feat := r.CatalogCount("tools.features")
	if feat != 3 {
		t.Fatalf("expected 3 for tools.features, got %d", feat)
	}

	none := r.CatalogCount("nonexistent")
	if none != 0 {
		t.Fatalf("expected 0 for nonexistent plugin, got %d", none)
	}
}

func TestSearchCatalog_ByName(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	results := r.SearchCatalog("feature")
	if len(results) != 3 {
		t.Fatalf("expected 3 results for 'feature', got %d", len(results))
	}
}

func TestSearchCatalog_ByDescription(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	results := r.SearchCatalog("marketplace")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'marketplace', got %d", len(results))
	}
}

func TestSearchCatalog_ExactMatchFirst(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	results := r.SearchCatalog("list_features")
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Name != "list_features" {
		t.Errorf("expected exact match first, got %q", results[0].Name)
	}
}

func TestSearchCatalog_CaseInsensitive(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	results := r.SearchCatalog("FEATURE")
	if len(results) != 3 {
		t.Fatalf("expected 3 results for case-insensitive 'FEATURE', got %d", len(results))
	}
}

func TestSearchCatalog_NoResults(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	results := r.SearchCatalog("nonexistent_xyz")
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestGetCatalogEntry_Found(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	entry := r.GetCatalogEntry("create_feature")
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Name != "create_feature" {
		t.Errorf("expected name 'create_feature', got %q", entry.Name)
	}
	if entry.PluginID != "tools.features" {
		t.Errorf("expected plugin 'tools.features', got %q", entry.PluginID)
	}
	if entry.Category != ToolCategoryGeneric {
		t.Errorf("expected category 'tool', got %q", entry.Category)
	}
	if entry.Schema == "" {
		t.Error("expected non-empty schema")
	}
}

func TestGetCatalogEntry_NotFound(t *testing.T) {
	r := NewRouter()

	entry := r.GetCatalogEntry("nonexistent")
	if entry != nil {
		t.Fatal("expected nil for nonexistent tool")
	}
}

func TestListPluginIDs(t *testing.T) {
	r := NewRouter()
	registerTestPlugin(r)

	ids := r.ListPluginIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 plugin IDs, got %d", len(ids))
	}
	// Should be sorted.
	if ids[0] != "tools.features" || ids[1] != "tools.marketplace" {
		t.Errorf("unexpected plugin IDs: %v", ids)
	}
}

func TestCatalog_AITools(t *testing.T) {
	r := NewRouter()

	manifest := &pluginv1.PluginManifest{
		ProvidesAi: []string{"claude"},
	}

	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID:       "bridge.claude",
		Manifest: manifest,
		Tools: []plugin.ExportedTool{
			{Name: "ai_prompt", Description: "Send a prompt to Claude", Schema: newTestSchema()},
		},
	})

	entries := r.ListCatalog("", 0, 100)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Category != ToolCategoryAI {
		t.Errorf("expected category 'ai', got %q", entry.Category)
	}
	if len(entry.Providers) != 1 || entry.Providers[0] != "claude" {
		t.Errorf("expected providers [claude], got %v", entry.Providers)
	}
	if entry.PluginID != "bridge.claude" {
		t.Errorf("expected plugin 'bridge.claude', got %q", entry.PluginID)
	}
}

func TestCatalog_ExternalPluginTools(t *testing.T) {
	r := NewRouter()

	r.RegisterExternal(&ExternalPlugin{
		ID: "engine.rag",
		ToolDefs: []*pluginv1.ToolDefinition{
			{Name: "parse_file", Description: "Parse a source file", InputSchema: newTestSchema()},
			{Name: "search", Description: "Search the index", InputSchema: newTestSchema()},
		},
	})

	entries := r.ListCatalog("engine.rag", 0, 100)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for engine.rag, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Category != ToolCategoryExternal {
			t.Errorf("expected category 'external', got %q", e.Category)
		}
		if e.PluginID != "engine.rag" {
			t.Errorf("expected plugin 'engine.rag', got %q", e.PluginID)
		}
	}
}
