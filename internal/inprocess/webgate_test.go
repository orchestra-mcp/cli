package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

// testRouter creates a Router with mock tools and a prompt registered.
func testRouter() *Router {
	r := NewRouter()

	schema, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]any{"type": "string"},
		},
	})

	builder := plugin.New("test-plugin")
	builder.RegisterTool("list_features", "List all features", schema, func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		result, _ := structpb.NewStruct(map[string]any{"text": "features listed"})
		return &pluginv1.ToolResponse{Success: true, Result: result}, nil
	})
	builder.RegisterTool("create_feature", "Create a feature", schema, func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		args := req.GetArguments()
		title := ""
		if args != nil {
			if v, ok := args.GetFields()["title"]; ok {
				title = v.GetStringValue()
			}
		}
		result, _ := structpb.NewStruct(map[string]any{"text": fmt.Sprintf("created: %s", title)})
		return &pluginv1.ToolResponse{Success: true, Result: result}, nil
	})
	builder.RegisterTool("failing_tool", "Always fails", nil, func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		return &pluginv1.ToolResponse{
			Success:      false,
			ErrorCode:    "validation_error",
			ErrorMessage: "title is required",
		}, nil
	})
	builder.RegisterPrompt("setup-project", "Set up a project", []*pluginv1.PromptArgument{
		{Name: "name", Required: true},
	}, func(ctx context.Context, req *pluginv1.PromptGetRequest) (*pluginv1.PromptGetResponse, error) {
		return &pluginv1.PromptGetResponse{
			Description: "Project setup guide",
			Messages: []*pluginv1.PromptMessage{
				{Role: "user", Content: &pluginv1.ContentBlock{Type: "text", Text: fmt.Sprintf("Set up %s.", req.GetArguments()["name"])}},
			},
		}, nil
	})

	ep := builder.Export()
	r.RegisterPlugin(ep)
	return r
}

// wgTestServer creates an httptest.Server with the WebGateServer handler.
func wgTestServer(t *testing.T, apiKey string, corsOrigins []string) (*httptest.Server, *WebGateServer) {
	t.Helper()
	router := testRouter()
	wg := NewWebGateServer(router, apiKey, "", "", corsOrigins)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)
	mux.HandleFunc("GET /health", wg.handleHealth)
	mux.HandleFunc("POST /register", wg.handleRegister)

	ts := httptest.NewServer(wg.corsMiddleware(mux))
	t.Cleanup(ts.Close)
	return ts, wg
}

// wsURL converts http:// to ws:// for WebSocket connections.
func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

// dialWS connects to the WebSocket endpoint.
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sendJSONRPC sends a JSON-RPC request and reads the response.
func sendJSONRPC(t *testing.T, conn *websocket.Conn, method string, id any, params any) protocol.JSONRPCResponse {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("write json: %v", err)
	}

	var resp protocol.JSONRPCResponse
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read json: %v", err)
	}
	return resp
}

// --- Health endpoint ---

func TestWebGateHealth(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status field: got %v", body["status"])
	}
	if body["transport"] != "web-gate" {
		t.Errorf("transport: got %v", body["transport"])
	}
	if body["tools"] == nil || body["tools"].(float64) < 1 {
		t.Errorf("tools: got %v", body["tools"])
	}
}

// --- WebSocket connection & basic methods ---

func TestWebGateInitialize(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "initialize", 1, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1.0.0"},
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var initResult protocol.MCPInitializeResult
	json.Unmarshal(resultBytes, &initResult)

	if initResult.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion: got %q", initResult.ProtocolVersion)
	}
	if initResult.ServerInfo.Name != "orchestra-web-gate" {
		t.Errorf("serverInfo.name: got %q", initResult.ServerInfo.Name)
	}
}

func TestWebGatePing(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "ping", 42, nil)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	resultBytes, _ := json.Marshal(resp.Result)
	if string(resultBytes) != "{}" {
		t.Errorf("ping result: got %s, want {}", string(resultBytes))
	}
}

// --- Tool operations ---

func TestWebGateToolsList(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "tools/list", 2, nil)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]any
	json.Unmarshal(resultBytes, &result)

	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array, got %T", result["tools"])
	}
	if len(tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d", len(tools))
	}

	// Verify tool names.
	names := make(map[string]bool)
	for _, tool := range tools {
		if tm, ok := tool.(map[string]any); ok {
			names[tm["name"].(string)] = true
		}
	}
	if !names["list_features"] {
		t.Error("missing list_features tool")
	}
	if !names["create_feature"] {
		t.Error("missing create_feature tool")
	}
}

func TestWebGateToolsCall(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "tools/call", 3, map[string]any{
		"name":      "create_feature",
		"arguments": map[string]any{"title": "my-feature"},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPToolResult
	json.Unmarshal(resultBytes, &result)

	if result.IsError {
		t.Error("expected IsError=false")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "my-feature") {
		t.Errorf("content: got %+v", result.Content)
	}
}

func TestWebGateToolsCallMissingName(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "tools/call", 4, map[string]any{
		"arguments": map[string]any{},
	})
	if resp.Error == nil {
		t.Fatal("expected error for missing tool name")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
}

func TestWebGateToolsCallError(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "tools/call", 5, map[string]any{
		"name": "failing_tool",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPToolResult
	json.Unmarshal(resultBytes, &result)

	if !result.IsError {
		t.Error("expected IsError=true")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "title is required") {
		t.Errorf("error text: got %+v", result.Content)
	}
}

func TestWebGateToolNotFound(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "tools/call", 6, map[string]any{
		"name": "nonexistent_tool",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPToolResult
	json.Unmarshal(resultBytes, &result)

	if !result.IsError {
		t.Error("expected IsError=true for nonexistent tool")
	}
}

// --- Prompt operations ---

func TestWebGatePromptsList(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "prompts/list", 7, nil)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]any
	json.Unmarshal(resultBytes, &result)

	prompts, ok := result["prompts"].([]any)
	if !ok || len(prompts) < 1 {
		t.Fatalf("expected at least 1 prompt, got %v", result["prompts"])
	}
}

func TestWebGatePromptsGet(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "prompts/get", 8, map[string]any{
		"name":      "setup-project",
		"arguments": map[string]any{"name": "demo"},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPPromptResult
	json.Unmarshal(resultBytes, &result)

	if result.Description != "Project setup guide" {
		t.Errorf("description: got %q", result.Description)
	}
	if len(result.Messages) != 1 || result.Messages[0].Role != "user" {
		t.Errorf("messages: got %+v", result.Messages)
	}
	if !strings.Contains(result.Messages[0].Content.Text, "demo") {
		t.Errorf("content text: got %q", result.Messages[0].Content.Text)
	}
}

func TestWebGatePromptsGetMissingName(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "prompts/get", 9, map[string]any{})
	if resp.Error == nil {
		t.Fatal("expected error for missing prompt name")
	}
	if resp.Error.Code != protocol.InvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.InvalidParams)
	}
}

// --- Method not found ---

func TestWebGateMethodNotFound(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "unknown/method", 10, nil)
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != protocol.MethodNotFound {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.MethodNotFound)
	}
}

// --- Invalid JSON ---

func TestWebGateInvalidJSON(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	conn.WriteMessage(websocket.TextMessage, []byte(`{invalid json}`))

	var resp protocol.JSONRPCResponse
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read json: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected parse error")
	}
	if resp.Error.Code != protocol.ParseError {
		t.Errorf("error code: got %d, want %d", resp.Error.Code, protocol.ParseError)
	}
}

// --- Multiple requests on same connection ---

func TestWebGateMultipleRequests(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	// Send 3 requests sequentially on the same connection.
	for i := 1; i <= 3; i++ {
		resp := sendJSONRPC(t, conn, "ping", i, nil)
		if resp.Error != nil {
			t.Errorf("request %d: unexpected error: %+v", i, resp.Error)
		}
	}
}

// --- Authentication ---

func TestWebGateAuthRequired(t *testing.T) {
	ts, _ := wgTestServer(t, "secret-key", nil)

	// No auth — should fail.
	_, httpResp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws"), nil)
	if err == nil {
		t.Fatal("expected dial to fail without auth")
	}
	if httpResp != nil && httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", httpResp.StatusCode, http.StatusUnauthorized)
	}
}

func TestWebGateAuthQueryParam(t *testing.T) {
	ts, _ := wgTestServer(t, "secret-key", nil)
	conn := dialWS(t, wsURL(ts, "/ws?api_key=secret-key"))

	resp := sendJSONRPC(t, conn, "ping", 1, nil)
	if resp.Error != nil {
		t.Errorf("expected success with query param auth: %+v", resp.Error)
	}
}

func TestWebGateAuthHeader(t *testing.T) {
	ts, _ := wgTestServer(t, "secret-key", nil)

	header := http.Header{}
	header.Set("X-API-Key", "secret-key")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws"), header)
	if err != nil {
		t.Fatalf("dial with header auth: %v", err)
	}
	defer conn.Close()

	resp := sendJSONRPC(t, conn, "ping", 1, nil)
	if resp.Error != nil {
		t.Errorf("expected success with header auth: %+v", resp.Error)
	}
}

func TestWebGateAuthBearer(t *testing.T) {
	ts, _ := wgTestServer(t, "secret-key", nil)

	header := http.Header{}
	header.Set("Authorization", "Bearer secret-key")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws"), header)
	if err != nil {
		t.Fatalf("dial with bearer auth: %v", err)
	}
	defer conn.Close()

	resp := sendJSONRPC(t, conn, "ping", 1, nil)
	if resp.Error != nil {
		t.Errorf("expected success with bearer auth: %+v", resp.Error)
	}
}

func TestWebGateAuthWrongKey(t *testing.T) {
	ts, _ := wgTestServer(t, "secret-key", nil)

	_, httpResp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/ws?api_key=wrong"), nil)
	if err == nil {
		t.Fatal("expected dial to fail with wrong key")
	}
	if httpResp != nil && httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", httpResp.StatusCode, http.StatusUnauthorized)
	}
}

func TestWebGateNoAuthRequired(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	resp := sendJSONRPC(t, conn, "ping", 1, nil)
	if resp.Error != nil {
		t.Errorf("expected success without auth when no key configured: %+v", resp.Error)
	}
}

// --- CORS ---

func TestWebGateCORSAllowAll(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/ws", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cors request: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Allow-Origin=*, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestWebGateCORSRestricted(t *testing.T) {
	ts, _ := wgTestServer(t, "", []string{"http://localhost:3000"})

	// Allowed origin.
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cors request: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected specific origin, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestWebGateCheckOrigin(t *testing.T) {
	wg := NewWebGateServer(NewRouter(), "", "", "", nil)

	// No origins configured — allow all.
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "http://anything.com")
	if !wg.checkOrigin(req) {
		t.Error("expected checkOrigin=true when no origins configured")
	}

	// With origins configured.
	wg.corsOrigins = []string{"http://localhost:3000", "https://app.example.com"}

	req.Header.Set("Origin", "http://localhost:3000")
	if !wg.checkOrigin(req) {
		t.Error("expected checkOrigin=true for allowed origin")
	}

	req.Header.Set("Origin", "http://evil.com")
	if wg.checkOrigin(req) {
		t.Error("expected checkOrigin=false for disallowed origin")
	}

	// No origin header — allow (non-browser client).
	req.Header.Del("Origin")
	if !wg.checkOrigin(req) {
		t.Error("expected checkOrigin=true for no origin (non-browser)")
	}
}

// --- External plugin routing (context bug regression test) ---

// mockExternalSender simulates a QUIC-connected external plugin that checks
// if the context is still valid when Send is called (like QUIC OpenStreamSync).
type mockExternalSender struct {
	callCount int
}

func (m *mockExternalSender) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	m.callCount++
	// This is the critical check: if the HTTP request context was passed through
	// instead of a server lifecycle context, ctx.Err() would be non-nil here
	// because net/http cancels request contexts after the handler returns
	// (which happens immediately after WebSocket upgrade).
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	result, _ := structpb.NewStruct(map[string]any{"text": "external plugin response"})
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_ToolCall{
			ToolCall: &pluginv1.ToolResponse{
				Success: true,
				Result:  result,
			},
		},
	}, nil
}

func TestWebGateExternalPluginContextNotCanceled(t *testing.T) {
	router := testRouter()
	sender := &mockExternalSender{}

	// Register an external plugin (simulates tools.agentops with list_accounts).
	schema, _ := structpb.NewStruct(map[string]any{"type": "object"})
	router.RegisterExternal(&ExternalPlugin{
		ID:     "tools.agentops",
		Client: sender,
		ToolDefs: []*pluginv1.ToolDefinition{
			{Name: "list_accounts", Description: "List AI accounts", InputSchema: schema},
		},
	})

	wg := NewWebGateServer(router, "", "", "", nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)
	ts := httptest.NewServer(wg.corsMiddleware(mux))
	t.Cleanup(func() {
		ts.Close()
		wg.cancel()
	})

	conn := dialWS(t, wsURL(ts, "/ws"))

	// Small delay to let the HTTP handler return (triggers r.Context() cancellation).
	time.Sleep(50 * time.Millisecond)

	resp := sendJSONRPC(t, conn, "tools/call", 1, map[string]any{
		"name": "list_accounts",
	})

	if resp.Error != nil {
		t.Fatalf("external plugin call failed: %v (this means the HTTP request context was passed to the QUIC client instead of the server context)", resp.Error.Message)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPToolResult
	json.Unmarshal(resultBytes, &result)

	if result.IsError {
		t.Errorf("expected IsError=false, got error: %+v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "external plugin response") {
		t.Errorf("unexpected content: %+v", result.Content)
	}
	if sender.callCount != 1 {
		t.Errorf("expected 1 call to external sender, got %d", sender.callCount)
	}
}

// --- Streaming tool call ---

// testRouterWithStreaming adds a streaming tool to testRouter for use in
// streaming tests.
func testRouterWithStreaming() *Router {
	r := testRouter()

	schema, _ := structpb.NewStruct(map[string]any{
		"type":     "object",
		"required": []any{"count"},
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
		},
	})

	builder := plugin.New("test-stream-plugin")
	builder.RegisterStreamingTool("count_chunks", "Emit N text chunks", schema,
		func(ctx context.Context, req *pluginv1.StreamStart, chunks chan<- []byte) error {
			n := 3
			if req.Arguments != nil {
				if v, ok := req.Arguments.GetFields()["count"]; ok {
					n = int(v.GetNumberValue())
				}
			}
			for i := 0; i < n; i++ {
				data, _ := json.Marshal(map[string]any{
					"type": "text_chunk",
					"text": fmt.Sprintf("chunk-%d", i),
				})
				select {
				case chunks <- data:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	)

	ep := builder.Export()
	r.RegisterPlugin(ep)
	return r
}

// wgStreamTestServer creates an httptest.Server with a router that has
// a streaming tool registered.
func wgStreamTestServer(t *testing.T) (*httptest.Server, *WebGateServer) {
	t.Helper()
	router := testRouterWithStreaming()
	wg := NewWebGateServer(router, "", "", "", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)

	ts := httptest.NewServer(wg.corsMiddleware(mux))
	t.Cleanup(ts.Close)
	return ts, wg
}

// TestWebGateStreamingChunksArriveBefore verifies that streaming chunk
// notifications (method=notifications/stream_chunk) are sent BEFORE the final
// JSON-RPC response for the streaming tool call.
//
// This is the core invariant that the Flutter client relies on: it must
// subscribe to notifications before initiating the streaming call, buffer
// chunks, then apply them after the final response resolves.
func TestWebGateStreamingChunksArriveBefore(t *testing.T) {
	ts, _ := wgStreamTestServer(t)
	conn := dialWS(t, wsURL(ts, "/ws"))

	// Send streaming tool call with streaming=true.
	const reqID = 100
	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "count_chunks",
			"arguments": map[string]any{"count": 3},
			"streaming": true,
		},
	}); err != nil {
		t.Fatalf("write streaming request: %v", err)
	}

	// Read all messages until we get the final response (has id=reqID).
	var chunks []map[string]any
	var finalResp protocol.JSONRPCResponse
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var msg json.RawMessage
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		msg = msgBytes

		var parsed map[string]any
		if err := json.Unmarshal(msg, &parsed); err != nil {
			t.Fatalf("parse message: %v", err)
		}

		// Check if this is the final response (has matching id).
		if id, ok := parsed["id"]; ok && id != nil {
			switch v := id.(type) {
			case float64:
				if int(v) == reqID {
					json.Unmarshal(msg, &finalResp)
					break
				}
			}
			if finalResp.JSONRPC != "" {
				break
			}
		}

		// Collect chunk notifications.
		if method, ok := parsed["method"].(string); ok && method == "notifications/stream_chunk" {
			chunks = append(chunks, parsed)
		}
	}

	// All 3 chunks must have arrived before the final response.
	if len(chunks) != 3 {
		t.Errorf("expected 3 stream chunks, got %d", len(chunks))
	}

	// Verify chunk structure.
	for i, chunk := range chunks {
		params, ok := chunk["params"].(map[string]any)
		if !ok {
			t.Errorf("chunk %d: missing params", i)
			continue
		}
		streamID, ok := params["stream_id"].(string)
		if !ok || streamID == "" {
			t.Errorf("chunk %d: missing stream_id", i)
		}
		if !strings.HasPrefix(streamID, "gate-st-") {
			t.Errorf("chunk %d: stream_id %q should start with 'gate-st-'", i, streamID)
		}
		data, ok := params["data"].(string)
		if !ok || data == "" {
			t.Errorf("chunk %d: missing data", i)
		}
		// Data should be JSON ChatEvent with type=text_chunk.
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Errorf("chunk %d: data is not JSON: %v", i, err)
		} else if event["type"] != "text_chunk" {
			t.Errorf("chunk %d: event type %q, want text_chunk", i, event["type"])
		}
	}

	// Verify final response includes stream_id and chunks_sent.
	if finalResp.JSONRPC == "" {
		t.Fatal("no final response received")
	}
	if finalResp.Error != nil {
		t.Fatalf("unexpected error in final response: %+v", finalResp.Error)
	}
	resultBytes, _ := json.Marshal(finalResp.Result)
	var result map[string]any
	json.Unmarshal(resultBytes, &result)
	if _, ok := result["stream_id"]; !ok {
		t.Error("final response missing stream_id field")
	}
	if chunksSent, ok := result["chunks_sent"]; !ok || chunksSent == nil {
		t.Error("final response missing chunks_sent field")
	}
}

// TestWebGateStreamingFallsThrough verifies that a streaming=true request for
// a non-streaming tool falls through to the regular tool call handler.
func TestWebGateStreamingFallsThrough(t *testing.T) {
	ts, _ := wgStreamTestServer(t)
	conn := dialWS(t, wsURL(ts, "/ws"))

	// list_features is a regular (non-streaming) tool.
	resp := sendJSONRPC(t, conn, "tools/call", 200, map[string]any{
		"name":      "list_features",
		"streaming": true,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	resultBytes, _ := json.Marshal(resp.Result)
	var result protocol.MCPToolResult
	json.Unmarshal(resultBytes, &result)
	if result.IsError {
		t.Errorf("expected IsError=false for fallthrough to regular tool")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "features listed") {
		t.Errorf("unexpected content: %+v", result.Content)
	}
}

// TestWebGateStreamingStreamID verifies the stream_id in chunk notifications
// matches the gate-st-{requestID} format.
func TestWebGateStreamingStreamIDFormat(t *testing.T) {
	ts, _ := wgStreamTestServer(t)
	conn := dialWS(t, wsURL(ts, "/ws"))

	const reqID = 42
	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "count_chunks",
			"arguments": map[string]any{"count": 1},
			"streaming": true,
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	expectedStreamID := fmt.Sprintf("gate-st-%d", reqID)
	deadline := time.Now().Add(5 * time.Second)
	foundChunk := false

	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var parsed map[string]any
		if err := conn.ReadJSON(&parsed); err != nil {
			t.Fatalf("read: %v", err)
		}

		if method, _ := parsed["method"].(string); method == "notifications/stream_chunk" {
			params, _ := parsed["params"].(map[string]any)
			if params != nil {
				if sid, _ := params["stream_id"].(string); sid == expectedStreamID {
					foundChunk = true
				}
			}
		}

		// Stop after seeing the final response.
		if id, _ := parsed["id"].(float64); int(id) == reqID {
			break
		}
	}

	if !foundChunk {
		t.Errorf("expected chunk with stream_id=%q, none found", expectedStreamID)
	}
}

// --- Notification (no response) ---

func TestWebGateNotification(t *testing.T) {
	ts, _ := wgTestServer(t, "", nil)
	conn := dialWS(t, wsURL(ts, "/ws"))

	// Send notification (no ID).
	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Notification gets no response. Send a ping to verify connection is alive.
	resp := sendJSONRPC(t, conn, "ping", 99, nil)
	if resp.Error != nil {
		t.Errorf("expected ping success after notification: %+v", resp.Error)
	}
}
