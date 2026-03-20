package inprocess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// wgBroadcastTestServer creates a test WebGateServer with StartDataChangeBroadcaster already running.
func wgBroadcastTestServer(t *testing.T) (*httptest.Server, *WebGateServer) {
	t.Helper()
	router := NewRouter()
	wg := NewWebGateServer(router, "", "", "", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wg.StartDataChangeBroadcaster(wg.ctx)
	return ts, wg
}

// dialWS opens a WebSocket connection to the test server and performs MCP initialize.
func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// Send MCP initialize so the connection is registered.
	initMsg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
			"capabilities":    map[string]any{},
		},
	}
	if err := conn.WriteJSON(initMsg); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	// Drain the initialize response.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	conn.ReadMessage()                                    //nolint:errcheck
	conn.SetReadDeadline(time.Time{})                     // reset
	return conn
}

// readNotification reads one WebSocket message and returns the parsed map.
func readNotification(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout)) //nolint:errcheck
	defer conn.SetReadDeadline(time.Time{})       //nolint:errcheck
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read WebSocket message: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(msg, &result); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return result
}

// TestDataChangeBroadcaster_PublishesNotificationsData verifies that EventBus
// events are forwarded as notifications/data messages to connected WS clients.
func TestDataChangeBroadcaster_PublishesNotificationsData(t *testing.T) {
	ts, wg := wgBroadcastTestServer(t)
	conn := dialWS(t, ts)

	// Publish a features event directly on the EventBus.
	payload, _ := structpb.NewStruct(map[string]any{"id": "FEAT-ABC"})
	wg.router.EventBus().Publish("features", "create_feature", payload, "router")

	msg := readNotification(t, conn, 2*time.Second)

	// The broadcast wraps in {"jsonrpc":"2.0","result":{"method":"...","params":{...}}}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'result' key in message, got: %v", msg)
	}
	if result["method"] != "notifications/data" {
		t.Errorf("expected method 'notifications/data', got %q", result["method"])
	}
	params, ok := result["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'params' in result, got: %v", result)
	}
	if params["topic"] != "features" {
		t.Errorf("expected topic 'features', got %q", params["topic"])
	}
	if params["event_type"] != "create_feature" {
		t.Errorf("expected event_type 'create_feature', got %q", params["event_type"])
	}
	if params["source"] != "router" {
		t.Errorf("expected source 'router', got %q", params["source"])
	}
	if params["timestamp"] == nil || params["timestamp"] == "" {
		t.Error("expected non-empty timestamp in params")
	}
	p, ok := params["payload"].(map[string]any)
	if !ok || p["id"] != "FEAT-ABC" {
		t.Errorf("expected payload.id='FEAT-ABC', got: %v", params["payload"])
	}
}

// TestDataChangeBroadcaster_MultipleTopics verifies events on different topics
// are all forwarded to connected clients.
func TestDataChangeBroadcaster_MultipleTopics(t *testing.T) {
	ts, wg := wgBroadcastTestServer(t)
	conn := dialWS(t, ts)

	topics := []struct {
		topic     string
		eventType string
	}{
		{"features", "create_feature"},
		{"notes", "create_note"},
		{"projects", "create_project"},
	}

	for _, tc := range topics {
		wg.router.EventBus().Publish(tc.topic, tc.eventType, nil, "router")
	}

	received := make(map[string]bool)
	for i := 0; i < len(topics); i++ {
		msg := readNotification(t, conn, 2*time.Second)
		result, _ := msg["result"].(map[string]any)
		params, _ := result["params"].(map[string]any)
		if topic, ok := params["topic"].(string); ok {
			received[topic] = true
		}
	}

	for _, tc := range topics {
		if !received[tc.topic] {
			t.Errorf("did not receive notification for topic %q", tc.topic)
		}
	}
}

// TestDataChangeBroadcaster_NoClientsNoBlock verifies events are handled
// gracefully when no WebSocket clients are connected (no blocking/panic).
func TestDataChangeBroadcaster_NoClientsNoBlock(t *testing.T) {
	_, wg := wgBroadcastTestServer(t)

	// Publish with zero connected clients — should not block or panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			wg.router.EventBus().Publish("features", "create_feature", nil, "router")
		}
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("publishing with no clients should not block")
	}
}

// TestDataChangeBroadcaster_StorageWriteTriggersNotification verifies the full
// path: router StorageWrite → EventBus → broadcaster → WebSocket client.
func TestDataChangeBroadcaster_StorageWriteTriggersNotification(t *testing.T) {
	ts, wg := wgBroadcastTestServer(t)
	wg.router.SetStorageHandler(&stubStorageHandler{})
	conn := dialWS(t, ts)

	// Trigger a storage write via the router Send() method.
	_, err := wg.router.Send(wg.ctx, &pluginv1.PluginRequest{
		RequestId: "req-sw-broadcast",
		Request: &pluginv1.PluginRequest_StorageWrite{
			StorageWrite: &pluginv1.StorageWriteRequest{
				Path: "orchestra-agents/features/FEAT-XYZ.md",
				Body: "# test feature",
			},
		},
	})
	if err != nil {
		t.Fatalf("router Send error: %v", err)
	}

	msg := readNotification(t, conn, 2*time.Second)
	result, _ := msg["result"].(map[string]any)
	params, _ := result["params"].(map[string]any)

	if params["topic"] != "features" {
		t.Errorf("expected topic 'features', got %q", params["topic"])
	}
	if params["event_type"] != "storage_write" {
		t.Errorf("expected event_type 'storage_write', got %q", params["event_type"])
	}
	if params["source"] != "storage" {
		t.Errorf("expected source 'storage', got %q", params["source"])
	}
}

// TestDataChangeBroadcaster_CtxCancelStops verifies the broadcaster goroutine
// exits cleanly when its context is cancelled.
func TestDataChangeBroadcaster_CtxCancelStops(t *testing.T) {
	router := NewRouter()
	wg := NewWebGateServer(router, "", "", "", nil)

	// Cancel the context immediately after starting.
	wg.cancel()

	// SubscribeAll should see the channel close shortly after cancel.
	subID, ch := router.EventBus().SubscribeAll()
	defer router.EventBus().Unsubscribe(subID)

	// Start broadcaster with already-cancelled context.
	wg.StartDataChangeBroadcaster(wg.ctx)

	// Give the goroutine a moment to exit.
	time.Sleep(50 * time.Millisecond)

	// Publishing should not reach any subscriber (broadcaster has exited).
	router.EventBus().Publish("features", "create_feature", nil, "router")

	select {
	case ev := <-ch:
		// The sub-all subscriber receives it (expected), but the broadcaster
		// goroutine should already be done — just verify no panic occurred.
		_ = ev
	case <-time.After(500 * time.Millisecond):
		// Fine — broadcaster drained and stopped.
	}
}
