// Package inprocess — webgate adds a WebSocket JSON-RPC 2.0 gateway to
// orchestra serve, allowing browser clients to call MCP tools directly via
// the in-process Router. This turns each machine running `orchestra serve`
// into a remotely accessible tunnel.
//
// Unlike the QUIC bridge's wsbridge (which connects to an orchestrator over
// QUIC), the WebGateServer uses the Router directly — no network hop.
package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

// WebGateServer exposes the in-process Router over WebSocket for remote browser
// clients. Each WebSocket connection is a persistent session that can send
// multiple JSON-RPC 2.0 requests over the same connection.
type WebGateServer struct {
	router       *Router
	apiKey       string   // optional (empty = no auth)
	cloudURL     string   // cloud server URL for reverse tunnel (empty = disabled)
	workspace    string   // absolute workspace path
	corsOrigins  []string // allowed origins (empty = allow all)
	upgrader     websocket.Upgrader
	server       *http.Server
	listener     net.Listener
	tokenManager *TunnelTokenManager
	ctx          context.Context    // server lifecycle context
	cancel       context.CancelFunc // cancels ctx on shutdown

	// connRegistry tracks all connected WebSocket clients so we can push
	// server-initiated notifications (e.g. permission events).
	connsMu sync.Mutex
	conns   map[*wsConn]struct{}
}

// NewWebGateServer creates a WebGateServer. If apiKey is empty, authentication
// is disabled. corsOrigins controls which browser origins can connect (empty =
// allow all).
func NewWebGateServer(router *Router, apiKey, cloudURL, workspace string, corsOrigins []string) *WebGateServer {
	ctx, cancel := context.WithCancel(context.Background())
	wg := &WebGateServer{
		router:       router,
		apiKey:       apiKey,
		cloudURL:     cloudURL,
		workspace:    workspace,
		corsOrigins:  corsOrigins,
		tokenManager: NewTunnelTokenManager(),
		ctx:          ctx,
		cancel:       cancel,
		conns:        make(map[*wsConn]struct{}),
	}
	wg.upgrader = websocket.Upgrader{
		ReadBufferSize:  16 * 1024,
		WriteBufferSize: 16 * 1024,
		CheckOrigin:     wg.checkOrigin,
	}
	return wg
}

// Addr returns the actual listening address. Only valid after ListenAndServe.
func (wg *WebGateServer) Addr() string {
	if wg.listener != nil {
		return wg.listener.Addr().String()
	}
	return ""
}

// ListenAndServe starts the HTTP server with WebSocket upgrade support. It
// blocks until ctx is cancelled or a fatal error occurs.
func (wg *WebGateServer) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)
	mux.HandleFunc("GET /health", wg.handleHealth)
	mux.HandleFunc("POST /register", wg.handleRegister)
	mux.HandleFunc("POST /generate-token", wg.handleGenerateToken)

	wg.server = &http.Server{
		Handler:     wg.corsMiddleware(mux),
		IdleTimeout: 120 * time.Second,
		// No ReadTimeout — WebSocket connections are long-lived and the read
		// deadline is managed per-connection in handleConnection.
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web-gate listen %s: %w", addr, err)
	}
	wg.listener = ln
	log.Printf("[web-gate] listening on %s (WebSocket JSON-RPC gateway)", ln.Addr().String())

	// Start the permission event poller — polls get_pending_permission and
	// pushes results to all connected WebSocket clients.
	wg.StartPermissionPoller(ctx)

	// Start the session event poller — polls drain_session_events and
	// pushes real-time tool/text events to all connected WebSocket clients.
	wg.StartEventPoller(ctx)

	errCh := make(chan error, 1)
	go func() {
		errCh <- wg.server.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		wg.cancel() // cancel all WebSocket connection contexts
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return wg.server.Shutdown(shutCtx)
	case err := <-errCh:
		wg.cancel() // cancel all WebSocket connection contexts
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// TokenManager returns the tunnel token manager for external use (e.g. serve.go
// generates a token after startup and displays it in the terminal).
func (wg *WebGateServer) TokenManager() *TunnelTokenManager {
	return wg.tokenManager
}

// GenerateRegistrationToken creates a new tunnel registration token using the
// current gate address and tool count. Returns the raw base64 token string.
func (wg *WebGateServer) GenerateRegistrationToken() (string, *TunnelToken, error) {
	addr := wg.Addr()
	toolCount := len(wg.router.ListToolNames())
	raw, err := wg.tokenManager.GenerateToken(addr, wg.apiKey, wg.cloudURL, wg.workspace, toolCount)
	if err != nil {
		return "", nil, err
	}
	token, _ := DecodeToken(raw)
	return raw, token, nil
}

// handleHealth responds to GET /health with a JSON status.
func (wg *WebGateServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	toolCount := len(wg.router.ListToolNames())
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","transport":"web-gate","tools":%d}`, toolCount)
}

// handleRegister handles POST /register — the web backend calls this endpoint
// with the registration token to verify and consume it. On success, returns
// the decoded tunnel metadata. The token is one-time use.
func (wg *WebGateServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid_request","message":"invalid JSON body"}`)
		return
	}
	if body.Token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing_token","message":"token field is required"}`)
		return
	}

	token, err := wg.tokenManager.VerifyToken(body.Token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"invalid_token","message":"%s"}`, err.Error())
		return
	}

	// Return the verified tunnel info.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"verified": true,
		"tunnel": map[string]any{
			"hostname":     token.Hostname,
			"os":           token.OS,
			"arch":         token.Arch,
			"local_ip":     token.LocalIP,
			"gate_address": token.GateAddress,
			"tool_count":   token.ToolCount,
			"has_auth":     token.APIKeyHash != "",
		},
	})
}

// handleGenerateToken handles POST /generate-token — generates a new registration
// token using the gate's own TunnelTokenManager so that POST /register will
// recognise and consume it. Called by attached-mode CLI instances that reuse a
// running gate instead of starting their own.
func (wg *WebGateServer) handleGenerateToken(w http.ResponseWriter, r *http.Request) {
	raw, token, err := wg.GenerateRegistrationToken()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"generate_failed","message":"%s"}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":      raw,
		"hostname":   token.Hostname,
		"os":         token.OS,
		"arch":       token.Arch,
		"local_ip":   token.LocalIP,
		"gate_addr":  token.GateAddress,
		"tool_count": token.ToolCount,
	})
}

// handleUpgrade performs WebSocket upgrade and starts the connection handler.
func (wg *WebGateServer) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	// Check auth via query param or header before upgrade.
	if wg.apiKey != "" {
		key := r.URL.Query().Get("api_key")
		if key == "" {
			key = r.Header.Get("X-API-Key")
		}
		if key == "" {
			key = r.Header.Get("Authorization")
			if strings.HasPrefix(key, "Bearer ") {
				key = strings.TrimPrefix(key, "Bearer ")
			} else {
				key = ""
			}
		}
		if key != wg.apiKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	conn, err := wg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[web-gate] upgrade error: %v", err)
		return
	}

	log.Printf("[web-gate] WebSocket connection established from %s", r.RemoteAddr)

	// Use the server lifecycle context, NOT the HTTP request context.
	// After WebSocket upgrade, r.Context() gets canceled by net/http when the
	// handler returns, which would immediately cancel any QUIC stream opens to
	// external plugins with "open stream: context canceled".
	connCtx, connCancel := context.WithCancel(wg.ctx)
	go func() {
		defer connCancel()
		wsc := &wsConn{Conn: conn}
		wg.registerConn(wsc)
		defer wg.unregisterConn(wsc)
		wg.handleConnectionWS(connCtx, wsc)
	}()
}

// wsConn wraps a gorilla/websocket conn with a write mutex for concurrent safety.
type wsConn struct {
	*websocket.Conn
	writeMu sync.Mutex
}

func (c *wsConn) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteJSON(v)
}

// registerConn adds a WebSocket connection to the registry for server-push.
func (wg *WebGateServer) registerConn(c *wsConn) {
	wg.connsMu.Lock()
	wg.conns[c] = struct{}{}
	wg.connsMu.Unlock()
}

// unregisterConn removes a WebSocket connection from the registry.
func (wg *WebGateServer) unregisterConn(c *wsConn) {
	wg.connsMu.Lock()
	delete(wg.conns, c)
	wg.connsMu.Unlock()
}

// broadcast sends a JSON-RPC notification to all connected WebSocket clients.
func (wg *WebGateServer) broadcast(method string, params any) {
	msg := protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"method": method, "params": params},
	}
	wg.connsMu.Lock()
	snapshot := make([]*wsConn, 0, len(wg.conns))
	for c := range wg.conns {
		snapshot = append(snapshot, c)
	}
	wg.connsMu.Unlock()

	for _, c := range snapshot {
		_ = c.writeJSON(msg)
	}
}

// StartPermissionPoller starts a background goroutine that polls
// get_pending_permission every second and pushes any pending requests to all
// connected WebSocket clients as server-initiated notifications. This bridges
// the gap where bridge-claude (external QUIC plugin) captures permission events
// from Claude CLI processes but has no way to push them to the browser.
func (wg *WebGateServer) StartPermissionPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wg.pollAndPushPermissions(ctx)
			}
		}
	}()
}

// pollAndPushPermissions calls get_pending_permission and broadcasts any
// pending requests to all connected WebSocket clients.
func (wg *WebGateServer) pollAndPushPermissions(ctx context.Context) {
	// Don't poll if nobody is connected.
	wg.connsMu.Lock()
	nConns := len(wg.conns)
	wg.connsMu.Unlock()
	if nConns == 0 {
		return
	}

	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-perm-poll-%d", time.Now().UnixMilli()),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:     "get_pending_permission",
				CallerPlugin: "web-gate-poller",
			},
		},
	})
	if err != nil {
		return // tool not available (bridge-claude not running)
	}

	tc := resp.GetToolCall()
	if tc == nil || !tc.GetSuccess() {
		return
	}

	text := ""
	if tc.GetResult() != nil {
		if v, ok := tc.GetResult().GetFields()["text"]; ok {
			text = v.GetStringValue()
		}
	}
	if text == "" || text == "[]" {
		return
	}

	// Parse the JSON array of pending permissions.
	var pending []json.RawMessage
	if err := json.Unmarshal([]byte(text), &pending); err != nil || len(pending) == 0 {
		return
	}

	// Broadcast each pending permission as a server-push notification.
	wg.broadcast("notifications/permission", json.RawMessage(text))
}

// StartEventPoller starts a background goroutine that polls
// drain_session_events every 200ms and pushes any pending events to all
// connected WebSocket clients as server-initiated notifications. This enables
// real-time streaming of tool cards and text in the web copilot.
func (wg *WebGateServer) StartEventPoller(ctx context.Context) {
	log.Printf("[web-gate] event poller started (200ms interval)")
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wg.pollAndPushEvents(ctx)
			}
		}
	}()
}

// pollAndPushEvents calls drain_session_events and broadcasts any pending
// events to all connected WebSocket clients.
func (wg *WebGateServer) pollAndPushEvents(ctx context.Context) {
	// Don't poll if nobody is connected.
	wg.connsMu.Lock()
	nConns := len(wg.conns)
	wg.connsMu.Unlock()
	if nConns == 0 {
		return
	}

	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-event-poll-%d", time.Now().UnixMilli()),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:     "drain_session_events",
				CallerPlugin: "web-gate-poller",
			},
		},
	})
	if err != nil {
		// Log every 30s to avoid flooding.
		if time.Now().Unix()%30 == 0 {
			log.Printf("[web-gate] event poll error: %v", err)
		}
		return // tool not available (bridge-claude not running)
	}

	tc := resp.GetToolCall()
	if tc == nil || !tc.GetSuccess() {
		if tc != nil && tc.GetErrorCode() != "" {
			log.Printf("[web-gate] event poll tool error: %s: %s", tc.GetErrorCode(), tc.GetErrorMessage())
		}
		return
	}

	text := ""
	if tc.GetResult() != nil {
		if v, ok := tc.GetResult().GetFields()["text"]; ok {
			text = v.GetStringValue()
		}
	}
	if text == "" || text == "[]" {
		return
	}

	// Parse the JSON array of events.
	var events []json.RawMessage
	if err := json.Unmarshal([]byte(text), &events); err != nil || len(events) == 0 {
		return
	}

	log.Printf("[web-gate] broadcasting %d session events to %d clients", len(events), nConns)

	// Broadcast events to all connected WebSocket clients.
	wg.broadcast("notifications/events", json.RawMessage(text))
}

// handleConnectionWS is the main message loop for a WebSocket connection.
// It takes a pre-wrapped *wsConn (already tracked in the connection registry).
func (wg *WebGateServer) handleConnectionWS(ctx context.Context, conn *wsConn) {
	defer conn.Close()

	// Set up keep-alive.
	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		return nil
	})

	// Ping goroutine.
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				conn.writeMu.Unlock()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { <-pingDone }()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[web-gate] read error: %v", err)
			}
			return
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))

		var req protocol.JSONRPCRequest
		if err := json.Unmarshal(message, &req); err != nil {
			conn.writeJSON(protocol.JSONRPCResponse{
				JSONRPC: "2.0",
				Error: &protocol.JSONRPCError{
					Code:    protocol.ParseError,
					Message: fmt.Sprintf("parse error: %v", err),
				},
			})
			continue
		}

		// Dispatch in a goroutine so the read loop is not blocked by
		// long-running tool calls (e.g. send_message waits minutes).
		// This allows concurrent requests like respond_permission to be
		// processed while send_message is still running.
		go func(r protocol.JSONRPCRequest) {
			resp := wg.dispatch(ctx, &r, conn)
			if resp != nil {
				if r.Method == "tools/call" {
					var p wgToolCallParams
					if r.Params != nil {
						_ = json.Unmarshal(r.Params, &p)
					}
					if p.Name == "respond_permission" || p.Name == "get_pending_permission" {
						log.Printf("[web-gate] %s dispatch done (id=%v), writing response", p.Name, r.ID)
					}
				}
				conn.writeJSON(resp)
			}
		}(req)
	}
}

// dispatch routes a JSON-RPC request to the appropriate handler.
func (wg *WebGateServer) dispatch(ctx context.Context, req *protocol.JSONRPCRequest, conn *wsConn) *protocol.JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return wg.handleInitialize(req)
	case "ping":
		return &protocol.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return wg.handleToolsList(ctx, req)
	case "tools/call":
		return wg.handleToolsCall(ctx, req, conn)
	case "prompts/list":
		return wg.handlePromptsList(ctx, req)
	case "prompts/get":
		return wg.handlePromptsGet(ctx, req)
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil // notifications get no response
		}
		return &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.MethodNotFound,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

func (wg *WebGateServer) handleInitialize(req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: protocol.MCPInitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: protocol.MCPServerCapabilities{
				Tools:   &protocol.MCPToolsCapability{},
				Prompts: &protocol.MCPPromptsCapability{},
			},
			ServerInfo: protocol.MCPServerInfo{
				Name:    "orchestra-web-gate",
				Version: "1.0.0",
			},
		},
	}
}

func (wg *WebGateServer) handleToolsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-lt-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err != nil {
		return wgErrResp(req.ID, protocol.InternalError, fmt.Sprintf("list_tools failed: %v", err))
	}

	lt := resp.GetListTools()
	if lt == nil {
		return wgErrResp(req.ID, protocol.InternalError, "unexpected response type")
	}

	mcpTools := make([]protocol.MCPToolDefinition, 0, len(lt.Tools))
	for _, td := range lt.Tools {
		mcpTools = append(mcpTools, wgToolDefToMCP(td))
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": mcpTools},
	}
}

type wgToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Streaming bool           `json:"streaming,omitempty"`
}

func (wg *WebGateServer) handleToolsCall(ctx context.Context, req *protocol.JSONRPCRequest, conn *wsConn) *protocol.JSONRPCResponse {
	var params wgToolCallParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return wgErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid params: %v", err))
		}
	}
	if params.Name == "" {
		return wgErrResp(req.ID, protocol.InvalidParams, "missing required parameter: name")
	}

	// Check for streaming handler.
	if params.Streaming {
		if handler, ok := wg.router.GetStreamHandler(params.Name); ok {
			return wg.handleStreaming(ctx, req, &params, conn, handler)
		}
		// Fall through to regular call if no streaming handler.
	}

	var args *structpb.Struct
	if params.Arguments != nil {
		var err error
		args, err = structpb.NewStruct(params.Arguments)
		if err != nil {
			return wgErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid arguments: %v", err))
		}
	}

	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-tc-%v", req.ID),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:     params.Name,
				Arguments:    args,
				CallerPlugin: "web-gate",
			},
		},
	})
	if err != nil {
		return wgErrResp(req.ID, protocol.InternalError, fmt.Sprintf("tool_call failed: %v", err))
	}

	tc := resp.GetToolCall()
	if tc == nil {
		return wgErrResp(req.ID, protocol.InternalError, "unexpected response type")
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  wgToolRespToMCP(tc),
	}
}

// handleStreaming handles a streaming tool call by writing chunks as JSON-RPC
// notifications over the WebSocket connection.
func (wg *WebGateServer) handleStreaming(ctx context.Context, req *protocol.JSONRPCRequest, params *wgToolCallParams, conn *wsConn, handler func(context.Context, *pluginv1.StreamStart, chan<- []byte) error) *protocol.JSONRPCResponse {
	streamID := fmt.Sprintf("gate-st-%v", req.ID)

	var args *structpb.Struct
	if params.Arguments != nil {
		var err error
		args, err = structpb.NewStruct(params.Arguments)
		if err != nil {
			return wgErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid arguments: %v", err))
		}
	}

	ss := &pluginv1.StreamStart{
		StreamId: streamID,
		ToolName: params.Name,
		Arguments: args,
	}

	chunks := make(chan []byte, 64)
	var sequence int64

	// Writer goroutine: sends chunks as notifications.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range chunks {
			conn.writeJSON(protocol.JSONRPCResponse{
				JSONRPC: "2.0",
				Result: map[string]any{
					"stream_id": streamID,
					"sequence":  sequence,
					"data":      string(data),
				},
			})
			sequence++
		}
	}()

	handlerErr := handler(ctx, ss, chunks)
	close(chunks)
	<-writerDone

	if handlerErr != nil {
		return wgErrResp(req.ID, protocol.InternalError, fmt.Sprintf("stream error: %v", handlerErr))
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("[streamed %d chunks]", sequence)},
			},
		},
	}
}

func (wg *WebGateServer) handlePromptsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-lp-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListPrompts{
			ListPrompts: &pluginv1.ListPromptsRequest{},
		},
	})
	if err != nil {
		return wgErrResp(req.ID, protocol.InternalError, fmt.Sprintf("list_prompts failed: %v", err))
	}

	lp := resp.GetListPrompts()
	if lp == nil {
		return wgErrResp(req.ID, protocol.InternalError, "unexpected response type")
	}

	mcpPrompts := make([]protocol.MCPPromptDefinition, 0, len(lp.Prompts))
	for _, pd := range lp.Prompts {
		args := make([]protocol.MCPPromptArgument, 0, len(pd.GetArguments()))
		for _, a := range pd.GetArguments() {
			args = append(args, protocol.MCPPromptArgument{
				Name:        a.GetName(),
				Description: a.GetDescription(),
				Required:    a.GetRequired(),
			})
		}
		mcpPrompts = append(mcpPrompts, protocol.MCPPromptDefinition{
			Name:        pd.GetName(),
			Description: pd.GetDescription(),
			Arguments:   args,
		})
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"prompts": mcpPrompts},
	}
}

type wgPromptGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

func (wg *WebGateServer) handlePromptsGet(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params wgPromptGetParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return wgErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid params: %v", err))
		}
	}
	if params.Name == "" {
		return wgErrResp(req.ID, protocol.InvalidParams, "missing required parameter: name")
	}

	resp, err := wg.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("gate-pg-%v", req.ID),
		Request: &pluginv1.PluginRequest_PromptGet{
			PromptGet: &pluginv1.PromptGetRequest{
				PromptName: params.Name,
				Arguments:  params.Arguments,
			},
		},
	})
	if err != nil {
		return wgErrResp(req.ID, protocol.InternalError, fmt.Sprintf("prompt_get failed: %v", err))
	}

	pg := resp.GetPromptGet()
	if pg == nil {
		return wgErrResp(req.ID, protocol.InternalError, "unexpected response type")
	}

	msgs := make([]protocol.MCPPromptMessage, 0, len(pg.GetMessages()))
	for _, m := range pg.GetMessages() {
		msg := protocol.MCPPromptMessage{Role: m.GetRole()}
		if c := m.GetContent(); c != nil {
			msg.Content = protocol.MCPContent{Type: c.GetType(), Text: c.GetText()}
		}
		msgs = append(msgs, msg)
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: protocol.MCPPromptResult{
			Description: pg.GetDescription(),
			Messages:    msgs,
		},
	}
}

// --- Helpers ---

func wgErrResp(id any, code int, message string) *protocol.JSONRPCResponse {
	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &protocol.JSONRPCError{Code: code, Message: message},
	}
}

func wgToolDefToMCP(td *pluginv1.ToolDefinition) protocol.MCPToolDefinition {
	var inputSchema any
	if td.GetInputSchema() != nil {
		inputSchema = wgStructToMap(td.GetInputSchema())
	}
	return protocol.MCPToolDefinition{
		Name:        td.GetName(),
		Description: td.GetDescription(),
		InputSchema: inputSchema,
	}
}

func wgToolRespToMCP(resp *pluginv1.ToolResponse) protocol.MCPToolResult {
	if !resp.GetSuccess() {
		errMsg := resp.GetErrorMessage()
		if errMsg == "" {
			errMsg = fmt.Sprintf("tool error: %s", resp.GetErrorCode())
		}
		return protocol.MCPToolResult{
			Content: []protocol.MCPContent{{Type: "text", Text: errMsg}},
			IsError: true,
		}
	}
	text := wgExtractResultText(resp.GetResult())
	return protocol.MCPToolResult{
		Content: []protocol.MCPContent{{Type: "text", Text: text}},
	}
}

func wgExtractResultText(s *structpb.Struct) string {
	if s == nil {
		return ""
	}
	if v, ok := s.GetFields()["text"]; ok {
		return v.GetStringValue()
	}
	m := wgStructToMap(s)
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf("%v", m)
	}
	return string(data)
}

func wgStructToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	result := make(map[string]any, len(s.GetFields()))
	for k, v := range s.GetFields() {
		result[k] = wgValueToInterface(v)
	}
	return result
}

func wgValueToInterface(v *structpb.Value) any {
	if v == nil {
		return nil
	}
	switch k := v.GetKind().(type) {
	case *structpb.Value_NullValue:
		return nil
	case *structpb.Value_NumberValue:
		return k.NumberValue
	case *structpb.Value_StringValue:
		return k.StringValue
	case *structpb.Value_BoolValue:
		return k.BoolValue
	case *structpb.Value_StructValue:
		return wgStructToMap(k.StructValue)
	case *structpb.Value_ListValue:
		if k.ListValue == nil {
			return nil
		}
		items := make([]any, len(k.ListValue.GetValues()))
		for i, item := range k.ListValue.GetValues() {
			items[i] = wgValueToInterface(item)
		}
		return items
	default:
		return nil
	}
}

// checkOrigin validates the WebSocket upgrade origin against corsOrigins.
func (wg *WebGateServer) checkOrigin(r *http.Request) bool {
	if len(wg.corsOrigins) == 0 {
		return true // allow all
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client
	}
	for _, allowed := range wg.corsOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

// corsMiddleware adds CORS headers to every response.
func (wg *WebGateServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if len(wg.corsOrigins) == 0 || origin == "" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			for _, allowed := range wg.corsOrigins {
				if allowed == "*" || allowed == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					break
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
