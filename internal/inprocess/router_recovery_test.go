package inprocess

import (
	"context"
	"testing"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSafeCallTool_NormalReturn(t *testing.T) {
	handler := func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		return &pluginv1.ToolResponse{
			Success: true,
		}, nil
	}
	req := &pluginv1.ToolRequest{ToolName: "test_tool"}

	result, err := safeCallTool(context.Background(), handler, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success=true")
	}
}

func TestSafeCallTool_PanicRecovery(t *testing.T) {
	handler := func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		panic("something went terribly wrong")
	}
	req := &pluginv1.ToolRequest{ToolName: "panicking_tool"}

	result, err := safeCallTool(context.Background(), handler, req)
	if err != nil {
		t.Fatalf("unexpected error (panic should be caught): %v", err)
	}
	if result.Success {
		t.Error("expected success=false after panic")
	}
	if result.ErrorCode != "panic" {
		t.Errorf("expected error code 'panic', got %q", result.ErrorCode)
	}
	if result.ErrorMessage == "" {
		t.Error("expected non-empty error message")
	}
}

func TestSafeCallTool_NilPointerPanic(t *testing.T) {
	handler := func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		var s *pluginv1.ToolResponse
		_ = s.Success // nil pointer dereference
		return s, nil
	}
	req := &pluginv1.ToolRequest{ToolName: "nil_deref_tool"}

	result, err := safeCallTool(context.Background(), handler, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ErrorCode != "panic" {
		t.Errorf("expected error code 'panic', got %q", result.ErrorCode)
	}
}

func TestSafeCallPrompt_NormalReturn(t *testing.T) {
	handler := func(ctx context.Context, req *pluginv1.PromptGetRequest) (*pluginv1.PromptGetResponse, error) {
		return &pluginv1.PromptGetResponse{
			Description: "test prompt",
		}, nil
	}
	req := &pluginv1.PromptGetRequest{PromptName: "test_prompt"}

	result, err := safeCallPrompt(context.Background(), handler, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Description != "test prompt" {
		t.Errorf("expected description 'test prompt', got %q", result.Description)
	}
}

func TestSafeCallPrompt_PanicRecovery(t *testing.T) {
	handler := func(ctx context.Context, req *pluginv1.PromptGetRequest) (*pluginv1.PromptGetResponse, error) {
		panic("prompt exploded")
	}
	req := &pluginv1.PromptGetRequest{PromptName: "panicking_prompt"}

	result, err := safeCallPrompt(context.Background(), handler, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Description == "" {
		t.Error("expected non-empty description with panic info")
	}
}

func TestRouteToolCall_PanicDoesNotCrashRouter(t *testing.T) {
	r := NewRouter()

	schema, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})

	r.RegisterPlugin(&plugin.ExportedPlugin{
		ID: "test-plugin",
		Tools: []plugin.ExportedTool{
			{
				Name:        "panicking_tool",
				Description: "A tool that panics",
				Schema:      schema,
				Handler: func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
					panic("boom")
				},
			},
			{
				Name:        "working_tool",
				Description: "A tool that works",
				Schema:      schema,
				Handler: func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
					return &pluginv1.ToolResponse{
						Success: true,
					}, nil
				},
			},
		},
	})

	// Call the panicking tool — should NOT crash the router.
	resp1, err := r.Send(context.Background(), &pluginv1.PluginRequest{
		RequestId: "req-1",
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{ToolName: "panicking_tool"},
		},
	})
	if err != nil {
		t.Fatalf("router returned error: %v", err)
	}
	toolResp1 := resp1.GetToolCall()
	if toolResp1 == nil {
		t.Fatal("expected tool call response")
	}
	if toolResp1.Success {
		t.Error("expected success=false for panicking tool")
	}
	if toolResp1.ErrorCode != "panic" {
		t.Errorf("expected error code 'panic', got %q", toolResp1.ErrorCode)
	}

	// Call the working tool AFTER the panic — router should still work.
	resp2, err := r.Send(context.Background(), &pluginv1.PluginRequest{
		RequestId: "req-2",
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{ToolName: "working_tool"},
		},
	})
	if err != nil {
		t.Fatalf("router returned error after panic: %v", err)
	}
	toolResp2 := resp2.GetToolCall()
	if toolResp2 == nil {
		t.Fatal("expected tool call response")
	}
	if !toolResp2.Success {
		t.Error("expected success=true for working tool after panic recovery")
	}
}

func TestRouterSend_CancelledContext(t *testing.T) {
	r := NewRouter()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Send(cancelledCtx, &pluginv1.PluginRequest{
		RequestId: "req-cancelled",
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}
