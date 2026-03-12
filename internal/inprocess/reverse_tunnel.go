package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/protocol"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// reverseReconnectMin is the minimum backoff for reconnection.
	reverseReconnectMin = time.Second

	// reverseReconnectMax is the maximum backoff for reconnection.
	reverseReconnectMax = 30 * time.Second

	// reverseWriteTimeout is the write deadline for the reverse connection.
	reverseWriteTimeout = 10 * time.Second
)

// TunnelLog writes a synchronized, newline-terminated log line to stderr.
// Color codes: 32=green, 33=yellow, 31=red, 36=cyan.
var tunnelLogMu sync.Mutex

func TunnelLog(color int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	tunnelLogMu.Lock()
	fmt.Fprintf(os.Stderr, "  \033[%dm[TUNNEL]\033[0m %s\n", color, msg)
	tunnelLogMu.Unlock()
}

// relayEnvelope is the message format on the reverse tunnel WebSocket.
// The cloud wraps browser requests with relay_to, and the local machine
// echoes it back so the cloud can route responses to the correct browser.
type relayEnvelope struct {
	RelayTo string          `json:"relay_to"`
	Type    string          `json:"type,omitempty"` // "close" when browser disconnects
	Message json.RawMessage `json:"message"`
}

// ReverseTunnelClient maintains a persistent outbound WebSocket connection
// from the local machine to the cloud server. It receives relay envelopes
// containing JSON-RPC requests from browser sessions, dispatches them through
// the in-process Router, and sends responses back.
type ReverseTunnelClient struct {
	cloudURL        string
	tunnelID        string
	connectionToken string
	router          *Router
	OnConnect       func(ctx context.Context) // called after each successful connect
}

// NewReverseTunnelClient creates a new reverse tunnel client.
func NewReverseTunnelClient(cloudURL, tunnelID, connectionToken string, router *Router) *ReverseTunnelClient {
	return &ReverseTunnelClient{
		cloudURL:        cloudURL,
		tunnelID:        tunnelID,
		connectionToken: connectionToken,
		router:          router,
	}
}

// ReconnectLoop connects to the cloud server and automatically reconnects
// with exponential backoff on failure. Blocks until ctx is cancelled.
func (rt *ReverseTunnelClient) ReconnectLoop(ctx context.Context) {
	backoff := reverseReconnectMin

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := rt.connect(ctx)
		if ctx.Err() != nil {
			TunnelLog(33, "Disconnected (shutting down)")
			return // Context cancelled — clean shutdown.
		}

		if err != nil {
			TunnelLog(31, "Disconnected: %v (reconnecting in %s)", err, backoff)
			log.Printf("[reverse-tunnel] disconnected: %v (reconnecting in %s)", err, backoff)
		} else {
			TunnelLog(33, "Disconnected (reconnecting in %s)", backoff)
			log.Printf("[reverse-tunnel] disconnected (reconnecting in %s)", backoff)
		}

		// Wait with jitter before reconnecting.
		jitter := time.Duration(float64(backoff) * (0.9 + 0.2*rand.Float64()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}

		// Exponential backoff.
		backoff *= 2
		if backoff > reverseReconnectMax {
			backoff = reverseReconnectMax
		}
	}
}

// connect establishes the reverse WebSocket and runs the message loop.
// Returns when the connection closes or an error occurs.
func (rt *ReverseTunnelClient) connect(ctx context.Context) error {
	wsURL := rt.buildURL()
	TunnelLog(33, "Connecting to %s ...", rt.cloudURL)
	log.Printf("[reverse-tunnel] connecting to %s", rt.cloudURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		TunnelLog(31, "Connection failed: %v", err)
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	TunnelLog(32, "Connected (tunnel %s)", rt.tunnelID)
	log.Printf("[reverse-tunnel] connected to %s (tunnel %s)", rt.cloudURL, rt.tunnelID)

	// Fire OnConnect callback in background so it doesn't block relay.
	if rt.OnConnect != nil {
		go rt.OnConnect(ctx)
	}

	// Reset backoff on successful connection (caller handles this via the loop).
	conn.SetReadLimit(1024 * 1024) // 1MB max message

	// Read loop: receive relay envelopes, dispatch, respond.
	for {
		select {
		case <-ctx.Done():
			conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutting down"),
				time.Now().Add(time.Second),
			)
			return nil
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var envelope relayEnvelope
		if err := json.Unmarshal(msg, &envelope); err != nil {
			log.Printf("[reverse-tunnel] invalid envelope: %v", err)
			continue
		}

		// Handle browser disconnect notification.
		if envelope.Type == "close" {
			continue // Nothing to clean up on the local side.
		}

		if envelope.RelayTo == "" || len(envelope.Message) == 0 {
			continue
		}

		// Dispatch the JSON-RPC request through the Router.
		response := rt.dispatch(ctx, envelope.Message)

		// Wrap the response and send it back.
		respEnvelope := relayEnvelope{
			RelayTo: envelope.RelayTo,
			Message: response,
		}

		conn.SetWriteDeadline(time.Now().Add(reverseWriteTimeout))
		if err := conn.WriteJSON(respEnvelope); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
}

// dispatch processes a raw JSON-RPC request through the Router and returns
// the JSON-RPC response as raw bytes.
func (rt *ReverseTunnelClient) dispatch(ctx context.Context, rawReq json.RawMessage) json.RawMessage {
	var req protocol.JSONRPCRequest
	if err := json.Unmarshal(rawReq, &req); err != nil {
		resp := protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &protocol.JSONRPCError{
				Code:    protocol.ParseError,
				Message: fmt.Sprintf("parse error: %v", err),
			},
		}
		b, _ := json.Marshal(resp)
		return b
	}

	var resp *protocol.JSONRPCResponse

	switch req.Method {
	case "initialize":
		resp = &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: protocol.MCPInitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: protocol.MCPServerCapabilities{
					Tools:   &protocol.MCPToolsCapability{},
					Prompts: &protocol.MCPPromptsCapability{},
				},
				ServerInfo: protocol.MCPServerInfo{
					Name:    "orchestra-reverse-tunnel",
					Version: "1.0.0",
				},
			},
		}

	case "ping":
		resp = &protocol.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}

	case "tools/list":
		resp = rt.handleToolsList(ctx, &req)

	case "tools/call":
		resp = rt.handleToolsCall(ctx, &req)

	case "prompts/list":
		resp = rt.handlePromptsList(ctx, &req)

	case "prompts/get":
		resp = rt.handlePromptsGet(ctx, &req)

	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil // Notifications get no response.
		}
		resp = &protocol.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &protocol.JSONRPCError{
				Code:    protocol.MethodNotFound,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}

	if resp == nil {
		return nil
	}
	b, _ := json.Marshal(resp)
	return b
}

func (rt *ReverseTunnelClient) handleToolsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := rt.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("rtun-lt-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err != nil {
		return rtErrResp(req.ID, protocol.InternalError, fmt.Sprintf("list_tools failed: %v", err))
	}

	lt := resp.GetListTools()
	if lt == nil {
		return rtErrResp(req.ID, protocol.InternalError, "unexpected response type")
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

type rtToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func (rt *ReverseTunnelClient) handleToolsCall(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params rtToolCallParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rtErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid params: %v", err))
		}
	}
	if params.Name == "" {
		return rtErrResp(req.ID, protocol.InvalidParams, "missing required parameter: name")
	}

	TunnelLog(36, "← tools/call %s", params.Name)

	var args *structpb.Struct
	if params.Arguments != nil {
		var err error
		args, err = structpb.NewStruct(params.Arguments)
		if err != nil {
			return rtErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid arguments: %v", err))
		}
	}

	resp, err := rt.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("rtun-tc-%v", req.ID),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:     params.Name,
				Arguments:    args,
				CallerPlugin: "reverse-tunnel",
			},
		},
	})
	if err != nil {
		return rtErrResp(req.ID, protocol.InternalError, fmt.Sprintf("tool_call failed: %v", err))
	}

	tc := resp.GetToolCall()
	if tc == nil {
		return rtErrResp(req.ID, protocol.InternalError, "unexpected response type")
	}

	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  wgToolRespToMCP(tc),
	}
}

func (rt *ReverseTunnelClient) handlePromptsList(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	resp, err := rt.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("rtun-pl-%v", req.ID),
		Request: &pluginv1.PluginRequest_ListPrompts{
			ListPrompts: &pluginv1.ListPromptsRequest{},
		},
	})
	if err != nil {
		return rtErrResp(req.ID, protocol.InternalError, fmt.Sprintf("list_prompts failed: %v", err))
	}

	lp := resp.GetListPrompts()
	if lp == nil {
		return rtErrResp(req.ID, protocol.InternalError, "unexpected response type")
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

func (rt *ReverseTunnelClient) handlePromptsGet(ctx context.Context, req *protocol.JSONRPCRequest) *protocol.JSONRPCResponse {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rtErrResp(req.ID, protocol.InvalidParams, fmt.Sprintf("invalid params: %v", err))
		}
	}
	if params.Name == "" {
		return rtErrResp(req.ID, protocol.InvalidParams, "missing required parameter: name")
	}

	resp, err := rt.router.Send(ctx, &pluginv1.PluginRequest{
		RequestId: fmt.Sprintf("rtun-pg-%v", req.ID),
		Request: &pluginv1.PluginRequest_PromptGet{
			PromptGet: &pluginv1.PromptGetRequest{
				PromptName: params.Name,
				Arguments:  params.Arguments,
			},
		},
	})
	if err != nil {
		return rtErrResp(req.ID, protocol.InternalError, fmt.Sprintf("get_prompt failed: %v", err))
	}

	pg := resp.GetPromptGet()
	if pg == nil {
		return rtErrResp(req.ID, protocol.InternalError, "unexpected response type")
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

// buildURL constructs the reverse tunnel WebSocket URL.
func (rt *ReverseTunnelClient) buildURL() string {
	// Convert https:// → wss://, http:// → ws://
	wsURL := rt.cloudURL
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	params := url.Values{}
	params.Set("tunnel_id", rt.tunnelID)
	params.Set("connection_token", rt.connectionToken)

	return fmt.Sprintf("%s/api/tunnels/reverse?%s", wsURL, params.Encode())
}

func rtErrResp(id any, code int, msg string) *protocol.JSONRPCResponse {
	return &protocol.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &protocol.JSONRPCError{
			Code:    code,
			Message: msg,
		},
	}
}
