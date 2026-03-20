package inprocess

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

// newTestRouter builds a Router with a stub storage handler that always
// reports success, so storage-path event tests work without a real filesystem.
func newTestRouter() *Router {
	r := NewRouter()
	r.SetStorageHandler(&stubStorageHandler{})
	return r
}

// stubStorageHandler is a minimal StorageHandler that returns success for
// writes and deletes without touching the filesystem.
type stubStorageHandler struct{}

func (s *stubStorageHandler) Read(_ context.Context, req *pluginv1.StorageReadRequest) (*pluginv1.StorageReadResponse, error) {
	return &pluginv1.StorageReadResponse{}, nil
}

func (s *stubStorageHandler) Write(_ context.Context, req *pluginv1.StorageWriteRequest) (*pluginv1.StorageWriteResponse, error) {
	return &pluginv1.StorageWriteResponse{Success: true}, nil
}

func (s *stubStorageHandler) Delete(_ context.Context, req *pluginv1.StorageDeleteRequest) (*pluginv1.StorageDeleteResponse, error) {
	return &pluginv1.StorageDeleteResponse{Success: true}, nil
}

func (s *stubStorageHandler) List(_ context.Context, req *pluginv1.StorageListRequest) (*pluginv1.StorageListResponse, error) {
	return &pluginv1.StorageListResponse{}, nil
}

// --- autoPublishToolEvent tests ---

func TestAutoPublishToolEvent_KnownTool(t *testing.T) {
	r := newTestRouter()
	eb := r.EventBus()
	_, ch := eb.Subscribe("features")

	args, _ := structpb.NewStruct(map[string]any{"id": "FEAT-TEST"})
	r.autoPublishToolEvent("create_feature", args)

	select {
	case ev := <-ch:
		if ev.Topic != "features" {
			t.Errorf("expected topic 'features', got %q", ev.Topic)
		}
		if ev.EventType != "create_feature" {
			t.Errorf("expected event_type 'create_feature', got %q", ev.EventType)
		}
		if ev.SourcePlugin != "router" {
			t.Errorf("expected source_plugin 'router', got %q", ev.SourcePlugin)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool event")
	}
}

func TestAutoPublishToolEvent_UnknownTool(t *testing.T) {
	r := newTestRouter()
	eb := r.EventBus()
	subID, ch := eb.SubscribeAll()
	defer eb.Unsubscribe(subID)

	// Unknown tool — should NOT publish anything.
	r.autoPublishToolEvent("list_tools", nil)

	select {
	case ev := <-ch:
		t.Errorf("unexpected event for non-mutating tool: topic=%s type=%s", ev.Topic, ev.EventType)
	case <-time.After(50 * time.Millisecond):
		// expected — no event
	}
}

func TestAutoPublishToolEvent_AllNewTopics(t *testing.T) {
	cases := []struct {
		tool  string
		topic string
	}{
		{"delegate_feature", "delegations"},
		{"respond_delegation", "delegations"},
		{"create_request", "requests"},
		{"convert_request", "requests"},
		{"dismiss_request", "requests"},
		{"doc_create", "docs"},
		{"doc_update", "docs"},
		{"doc_delete", "docs"},
		{"create_agent", "agents"},
		{"update_agent", "agents"},
		{"delete_agent", "agents"},
		{"create_skill", "skills"},
		{"update_skill", "skills"},
		{"delete_skill", "skills"},
		{"create_workflow", "workflows"},
		{"update_workflow", "workflows"},
		{"delete_workflow", "workflows"},
		{"create_hook", "hooks"},
		{"update_hook", "hooks"},
		{"create_prompt", "prompts"},
		{"update_prompt", "prompts"},
		{"create_action", "actions"},
		{"update_action", "actions"},
		{"assign_feature", "features"},
		{"unassign_feature", "features"},
		{"tag_note", "notes"},
		{"create_experiment", "experiments"},
		{"start_experiment", "experiments"},
		{"create_hypothesis", "hypotheses"},
		{"validate_hypothesis", "hypotheses"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tool, func(t *testing.T) {
			r := newTestRouter()
			eb := r.EventBus()
			_, ch := eb.Subscribe(tc.topic)

			r.autoPublishToolEvent(tc.tool, nil)

			select {
			case ev := <-ch:
				if ev.Topic != tc.topic {
					t.Errorf("expected topic %q, got %q", tc.topic, ev.Topic)
				}
				if ev.EventType != tc.tool {
					t.Errorf("expected event_type %q, got %q", tc.tool, ev.EventType)
				}
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for event for tool %q → topic %q", tc.tool, tc.topic)
			}
		})
	}
}

// --- autoPublishStorageEvent tests ---

func TestAutoPublishStorageEvent_Features(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("features")

	r.autoPublishStorageEvent("orchestra-agents/features/FEAT-ABC.md", "storage_write")

	select {
	case ev := <-ch:
		if ev.Topic != "features" {
			t.Errorf("expected topic 'features', got %q", ev.Topic)
		}
		if ev.EventType != "storage_write" {
			t.Errorf("expected event_type 'storage_write', got %q", ev.EventType)
		}
		if ev.SourcePlugin != "storage" {
			t.Errorf("expected source_plugin 'storage', got %q", ev.SourcePlugin)
		}
		if ev.Payload == nil {
			t.Error("expected non-nil payload with path")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for storage event")
	}
}

func TestAutoPublishStorageEvent_Notes(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("notes")

	r.autoPublishStorageEvent(".notes/note-abc123.md", "storage_write")

	select {
	case ev := <-ch:
		if ev.Topic != "notes" {
			t.Errorf("expected topic 'notes', got %q", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notes storage event")
	}
}

func TestAutoPublishStorageEvent_Docs(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("docs")

	r.autoPublishStorageEvent("docs/my-project/architecture.md", "storage_write")

	select {
	case ev := <-ch:
		if ev.Topic != "docs" {
			t.Errorf("expected topic 'docs', got %q", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for docs storage event")
	}
}

func TestAutoPublishStorageEvent_UnknownPath(t *testing.T) {
	r := newTestRouter()
	subID, ch := r.EventBus().SubscribeAll()
	defer r.EventBus().Unsubscribe(subID)

	// Path that doesn't match any known topic prefix.
	r.autoPublishStorageEvent("tmp/unrelated/file.txt", "storage_write")

	select {
	case ev := <-ch:
		t.Errorf("unexpected event for unknown path: topic=%s", ev.Topic)
	case <-time.After(50 * time.Millisecond):
		// expected — no event
	}
}

func TestAutoPublishStorageEvent_Delete(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("plans")

	r.autoPublishStorageEvent("orchestra-agents/plans/PLAN-XYZ.md", "storage_delete")

	select {
	case ev := <-ch:
		if ev.Topic != "plans" {
			t.Errorf("expected topic 'plans', got %q", ev.Topic)
		}
		if ev.EventType != "storage_delete" {
			t.Errorf("expected event_type 'storage_delete', got %q", ev.EventType)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for storage delete event")
	}
}

// --- Router.Send integration: storage write triggers EventBus ---

func TestRouterSend_StorageWritePublishesEvent(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("features")

	_, err := r.Send(context.Background(), &pluginv1.PluginRequest{
		RequestId: "req-sw-1",
		Request: &pluginv1.PluginRequest_StorageWrite{
			StorageWrite: &pluginv1.StorageWriteRequest{
				Path: "orchestra-agents/features/FEAT-SW1.md",
				Body: "# test",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Topic != "features" {
			t.Errorf("expected topic 'features', got %q", ev.Topic)
		}
		if ev.EventType != "storage_write" {
			t.Errorf("expected event_type 'storage_write', got %q", ev.EventType)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: storage write should have published EventBus event")
	}
}

func TestRouterSend_StorageDeletePublishesEvent(t *testing.T) {
	r := newTestRouter()
	_, ch := r.EventBus().Subscribe("notes")

	_, err := r.Send(context.Background(), &pluginv1.PluginRequest{
		RequestId: "req-sd-1",
		Request: &pluginv1.PluginRequest_StorageDelete{
			StorageDelete: &pluginv1.StorageDeleteRequest{
				Path: ".notes/note-del1.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Topic != "notes" {
			t.Errorf("expected topic 'notes', got %q", ev.Topic)
		}
		if ev.EventType != "storage_delete" {
			t.Errorf("expected event_type 'storage_delete', got %q", ev.EventType)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: storage delete should have published EventBus event")
	}
}

// --- Router.Send integration: tool call triggers EventBus ---

func TestRouterSend_ToolCallPublishesEvent(t *testing.T) {
	r := newTestRouter()

	schema, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID: "test-features",
		Tools: []plugin.ExportedTool{
			{
				Name:        "create_feature",
				Description: "Create a feature",
				Schema:      schema,
				Handler: func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
					return &pluginv1.ToolResponse{Success: true}, nil
				},
			},
		},
	})

	_, ch := r.EventBus().Subscribe("features")

	_, err := r.Send(context.Background(), &pluginv1.PluginRequest{
		RequestId: "req-tc-1",
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{ToolName: "create_feature"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Topic != "features" {
			t.Errorf("expected topic 'features', got %q", ev.Topic)
		}
		if ev.EventType != "create_feature" {
			t.Errorf("expected event_type 'create_feature', got %q", ev.EventType)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: successful tool call should have published EventBus event")
	}
}

func TestRouterSend_FailedToolCallDoesNotPublish(t *testing.T) {
	r := newTestRouter()

	schema, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID: "test-features-fail",
		Tools: []plugin.ExportedTool{
			{
				Name:        "create_feature",
				Description: "Create a feature (fails)",
				Schema:      schema,
				Handler: func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
					return &pluginv1.ToolResponse{Success: false, ErrorCode: "validation_error"}, nil
				},
			},
		},
	})

	subID, ch := r.EventBus().SubscribeAll()
	defer r.EventBus().Unsubscribe(subID)

	r.Send(context.Background(), &pluginv1.PluginRequest{ //nolint:errcheck
		RequestId: "req-tc-fail",
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{ToolName: "create_feature"},
		},
	})

	select {
	case ev := <-ch:
		t.Errorf("unexpected event published after failed tool call: topic=%s", ev.Topic)
	case <-time.After(50 * time.Millisecond):
		// expected — no event for failed calls
	}
}
