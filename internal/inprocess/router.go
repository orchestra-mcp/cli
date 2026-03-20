// Package inprocess provides an in-process plugin router that dispatches tool
// calls, storage operations, and prompt requests via direct Go function calls
// instead of QUIC network round-trips. It implements the Sender interface used
// by StdioTransport and the StorageClient interface used by plugins.
package inprocess

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// ToolCategory classifies how a tool is registered in the router.
type ToolCategory = string

const (
	ToolCategoryGeneric  ToolCategory = "tool"
	ToolCategoryAI       ToolCategory = "ai"
	ToolCategoryStream   ToolCategory = "stream"
	ToolCategoryExternal ToolCategory = "external"
)

// CatalogEntry is an alias for the sdk-go plugin.CatalogEntry type.
type CatalogEntry = plugin.CatalogEntry


// Router dispatches PluginRequests to in-process tool handlers, storage
// handlers, and prompt handlers. It replaces the QUIC-based orchestrator
// router for local IDE use. External plugins (e.g. engine-rag) are supported
// via ExternalPlugin entries that forward requests over QUIC.
//
// Router implements the Sender interface:
//
//	Send(ctx, *PluginRequest) (*PluginResponse, error)
//
// This means it can be passed directly to StdioTransport and also used as the
// clientAdapter for plugins that need cross-plugin storage calls.
type Router struct {
	mu sync.RWMutex

	// toolHandlers maps toolName -> handler for regular tools.
	toolHandlers map[string]plugin.ToolHandler

	// streamHandlers maps toolName -> handler for streaming tools.
	streamHandlers map[string]plugin.StreamingToolHandler

	// promptHandlers maps promptName -> handler.
	promptHandlers map[string]plugin.PromptHandler

	// toolDefs maps toolName -> definition for ListTools responses.
	toolDefs map[string]*pluginv1.ToolDefinition

	// promptDefs maps promptName -> definition for ListPrompts responses.
	promptDefs map[string]*pluginv1.PromptDefinition

	// storageHandler handles storage read/write/delete/list operations.
	storageHandler plugin.StorageHandler

	// aiToolHandlers maps provider -> toolName -> handler.
	// AI bridge tools are NOT added to toolHandlers to avoid name collisions
	// (all bridges share the same tool names like ai_prompt, spawn_session).
	aiToolHandlers map[string]map[string]plugin.ToolHandler

	// aiToolDefs maps provider -> toolName -> definition.
	aiToolDefs map[string]map[string]*pluginv1.ToolDefinition

	// external maps pluginID -> ExternalPlugin for QUIC-connected plugins.
	external map[string]*ExternalPlugin

	// toolPluginMap maps toolName -> pluginID for catalog lookups.
	toolPluginMap map[string]string

	// toolCategoryMap maps toolName -> ToolCategory for catalog lookups.
	toolCategoryMap map[string]ToolCategory

	// toolProvidersMap maps toolName -> []provider for AI tools.
	toolProvidersMap map[string][]string

	// limiter enforces per-caller rate limits on tool calls.
	limiter *rateLimiter

	// metrics collects per-tool call statistics.
	metrics *Metrics

	// eventBus dispatches events to subscribers (desktop apps, hooks, etc.).
	eventBus *EventBus

	// onToolsChanged callbacks are invoked when tools are registered or
	// unregistered, so transports can send notifications/tools/list_changed.
	onToolsChanged []func()
}

// NewRouter creates a new in-process Router.
func NewRouter() *Router {
	return &Router{
		toolHandlers:    make(map[string]plugin.ToolHandler),
		streamHandlers:  make(map[string]plugin.StreamingToolHandler),
		promptHandlers:  make(map[string]plugin.PromptHandler),
		toolDefs:        make(map[string]*pluginv1.ToolDefinition),
		promptDefs:      make(map[string]*pluginv1.PromptDefinition),
		aiToolHandlers:  make(map[string]map[string]plugin.ToolHandler),
		aiToolDefs:      make(map[string]map[string]*pluginv1.ToolDefinition),
		external:        make(map[string]*ExternalPlugin),
		toolPluginMap:   make(map[string]string),
		toolCategoryMap: make(map[string]ToolCategory),
		toolProvidersMap: make(map[string][]string),
		limiter:         newRateLimiter(),
		metrics:         NewMetrics(),
		eventBus:        NewEventBus(),
	}
}

// SetStorageHandler sets the storage backend. Pass a DualStorage for dual-write
// (both Markdown and SQLite simultaneously).
func (r *Router) SetStorageHandler(h plugin.StorageHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storageHandler = h
}

// EventBus returns the router's event bus for subscribing to events.
func (r *Router) EventBus() *EventBus { return r.eventBus }

// OnToolsChanged registers a callback invoked when tools are added or removed.
// Used by transports to send notifications/tools/list_changed to clients.
func (r *Router) OnToolsChanged(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onToolsChanged = append(r.onToolsChanged, fn)
}

// notifyToolsChanged invokes all registered OnToolsChanged callbacks.
// Must NOT be called with r.mu held (callbacks may re-enter the router).
func (r *Router) notifyToolsChanged() {
	r.mu.RLock()
	callbacks := make([]func(), len(r.onToolsChanged))
	copy(callbacks, r.onToolsChanged)
	r.mu.RUnlock()

	for _, fn := range callbacks {
		fn()
	}
}

// RegisterPlugin registers all tools, streaming tools, and prompts from an
// ExportedPlugin. If the plugin's manifest declares ProvidesAI, tools are
// indexed under the AI routing table instead of the generic tool table.
func (r *Router) RegisterPlugin(ep *plugin.ExportedPlugin) {
	r.mu.Lock()

	isAI := len(ep.Manifest.GetProvidesAi()) > 0

	pluginID := ep.ID

	for _, t := range ep.Tools {
		def := &pluginv1.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		}
		if isAI {
			for _, provider := range ep.Manifest.GetProvidesAi() {
				if r.aiToolHandlers[provider] == nil {
					r.aiToolHandlers[provider] = make(map[string]plugin.ToolHandler)
					r.aiToolDefs[provider] = make(map[string]*pluginv1.ToolDefinition)
				}
				r.aiToolHandlers[provider][t.Name] = t.Handler
				r.aiToolDefs[provider][t.Name] = def
			}
			r.toolPluginMap[t.Name] = pluginID
			r.toolCategoryMap[t.Name] = ToolCategoryAI
			r.toolProvidersMap[t.Name] = ep.Manifest.GetProvidesAi()
		} else {
			r.toolHandlers[t.Name] = t.Handler
			r.toolDefs[t.Name] = def
			r.toolPluginMap[t.Name] = pluginID
			r.toolCategoryMap[t.Name] = ToolCategoryGeneric
		}
	}

	for _, st := range ep.StreamTools {
		def := &pluginv1.ToolDefinition{
			Name:        st.Name,
			Description: st.Description,
			InputSchema: st.Schema,
		}
		if isAI {
			for _, provider := range ep.Manifest.GetProvidesAi() {
				// Streaming tools go into AI defs for listing but we store
				// the handler separately since it has a different signature.
				if r.aiToolDefs[provider] == nil {
					r.aiToolDefs[provider] = make(map[string]*pluginv1.ToolDefinition)
				}
				r.aiToolDefs[provider][st.Name] = def
			}
			r.toolPluginMap[st.Name] = pluginID
			r.toolCategoryMap[st.Name] = ToolCategoryStream
			r.toolProvidersMap[st.Name] = ep.Manifest.GetProvidesAi()
		} else {
			r.toolDefs[st.Name] = def
			r.toolPluginMap[st.Name] = pluginID
			r.toolCategoryMap[st.Name] = ToolCategoryStream
		}
		r.streamHandlers[st.Name] = st.Handler
	}

	for _, pr := range ep.Prompts {
		r.promptDefs[pr.Name] = &pluginv1.PromptDefinition{
			Name:        pr.Name,
			Description: pr.Description,
			Arguments:   pr.Arguments,
		}
		r.promptHandlers[pr.Name] = pr.Handler
	}

	if ep.StorageHandler != nil {
		r.storageHandler = ep.StorageHandler
	}

	slog.Info("registered plugin", "plugin", ep.ID, "tools", len(ep.Tools)+len(ep.StreamTools), "prompts", len(ep.Prompts))
	r.mu.Unlock()

	r.notifyToolsChanged()
}

// RegisterExternal adds an external QUIC-connected plugin to the router.
// External plugins override in-process handlers for same-named tools,
// allowing installable plugins to supersede bundled core tool definitions.
func (r *Router) RegisterExternal(ep *ExternalPlugin) {
	r.mu.Lock()
	r.external[ep.ID] = ep

	for _, def := range ep.ToolDefs {
		if len(ep.ProvidesAI) > 0 {
			for _, provider := range ep.ProvidesAI {
				if r.aiToolDefs[provider] == nil {
					r.aiToolDefs[provider] = make(map[string]*pluginv1.ToolDefinition)
				}
				r.aiToolDefs[provider][def.Name] = def
			}
			r.toolPluginMap[def.Name] = ep.ID
			r.toolCategoryMap[def.Name] = ToolCategoryAI
			r.toolProvidersMap[def.Name] = ep.ProvidesAI
		} else {
			// Remove any in-process handler for this tool so the external
			// plugin takes priority via routeToolCall's fallback path.
			if _, exists := r.toolHandlers[def.Name]; exists {
				delete(r.toolHandlers, def.Name)
				slog.Info("external plugin overrides in-process tool", "plugin", ep.ID, "tool", def.Name)
			}
			r.toolDefs[def.Name] = def
			r.toolPluginMap[def.Name] = ep.ID
			r.toolCategoryMap[def.Name] = ToolCategoryExternal
		}
	}

	for _, def := range ep.PromptDefs {
		r.promptDefs[def.Name] = def
	}

	slog.Info("registered external plugin", "plugin", ep.ID, "tools", len(ep.ToolDefs))
	r.mu.Unlock()

	r.notifyToolsChanged()
}

// providerAliases maps OpenAI-compatible providers to "openai" so they route
// to bridge-openai when no dedicated bridge is registered.
var providerAliases = map[string]string{
	"grok":       "openai",
	"perplexity": "openai",
	"deepseek":   "openai",
	"qwen":       "openai",
	"kimi":       "openai",
}

// Send implements the Sender interface. It dispatches a PluginRequest to the
// appropriate in-process handler based on the request type.
func (r *Router) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled: %w", err)
	}

	switch rr := req.Request.(type) {

	case *pluginv1.PluginRequest_ToolCall:
		return r.routeToolCall(ctx, rr.ToolCall)

	case *pluginv1.PluginRequest_ListTools:
		return r.listTools(ctx)

	case *pluginv1.PluginRequest_ListPrompts:
		return r.listPrompts(ctx)

	case *pluginv1.PluginRequest_PromptGet:
		return r.routePromptGet(ctx, rr.PromptGet)

	case *pluginv1.PluginRequest_StorageRead:
		return r.routeStorageRead(ctx, rr.StorageRead)

	case *pluginv1.PluginRequest_StorageWrite:
		return r.routeStorageWrite(ctx, rr.StorageWrite)

	case *pluginv1.PluginRequest_StorageDelete:
		return r.routeStorageDelete(ctx, rr.StorageDelete)

	case *pluginv1.PluginRequest_StorageList:
		return r.routeStorageList(ctx, rr.StorageList)

	case *pluginv1.PluginRequest_Health:
		return &pluginv1.PluginResponse{
			RequestId: req.GetRequestId(),
			Response: &pluginv1.PluginResponse_Health{
				Health: &pluginv1.HealthResult{
					Status:  pluginv1.HealthResult_STATUS_HEALTHY,
					Message: "in-process router ok",
				},
			},
		}, nil

	case *pluginv1.PluginRequest_Register:
		return &pluginv1.PluginResponse{
			RequestId: req.GetRequestId(),
			Response: &pluginv1.PluginResponse_Register{
				Register: &pluginv1.RegistrationResult{Accepted: true},
			},
		}, nil

	case *pluginv1.PluginRequest_Publish:
		pub := rr.Publish
		r.eventBus.Publish(pub.GetTopic(), pub.GetEventType(), pub.GetPayload(), pub.GetSourcePlugin())
		return &pluginv1.PluginResponse{RequestId: req.GetRequestId()}, nil

	case *pluginv1.PluginRequest_Subscribe:
		sub := rr.Subscribe
		id, _ := r.eventBus.Subscribe(sub.GetTopic())
		return &pluginv1.PluginResponse{
			RequestId: req.GetRequestId(),
			Response: &pluginv1.PluginResponse_EventDelivery{
				EventDelivery: &pluginv1.EventDelivery{
					SubscriptionId: id,
					Topic:          sub.GetTopic(),
					EventType:      "subscribed",
				},
			},
		}, nil

	case *pluginv1.PluginRequest_Unsubscribe:
		r.eventBus.Unsubscribe(rr.Unsubscribe.GetSubscriptionId())
		return &pluginv1.PluginResponse{RequestId: req.GetRequestId()}, nil

	default:
		return &pluginv1.PluginResponse{
			RequestId: req.GetRequestId(),
			Response: &pluginv1.PluginResponse_ToolCall{
				ToolCall: &pluginv1.ToolResponse{
					Success:      false,
					ErrorCode:    "unknown_request",
					ErrorMessage: "unrecognized request type",
				},
			},
		}, nil
	}
}

// findToolHandler looks up a tool handler, using the same routing logic as the
// orchestrator: provider-specific AI routes first, then aliases, then claude
// default, then generic tool routes, then external plugins.
func (r *Router) findToolHandler(toolName, provider string) (plugin.ToolHandler, bool) {
	// Provider-specific AI route.
	if provider != "" {
		if handlers, ok := r.aiToolHandlers[provider]; ok {
			if h, ok := handlers[toolName]; ok {
				return h, true
			}
		}
		// Alias fallback (e.g. deepseek -> openai).
		if alias, ok := providerAliases[provider]; ok {
			if handlers, ok := r.aiToolHandlers[alias]; ok {
				if h, ok := handlers[toolName]; ok {
					return h, true
				}
			}
		}
	}
	// Default to claude for any provider lookup that didn't match.
	if provider != "" {
		if handlers, ok := r.aiToolHandlers["claude"]; ok {
			if h, ok := handlers[toolName]; ok {
				return h, true
			}
		}
	}
	// No provider specified — check if tool exists in AI routes (default claude).
	if provider == "" {
		if handlers, ok := r.aiToolHandlers["claude"]; ok {
			if h, ok := handlers[toolName]; ok {
				return h, true
			}
		}
	}
	// Generic tool routes.
	if h, ok := r.toolHandlers[toolName]; ok {
		return h, true
	}
	return nil, false
}

// findExternalPlugin looks for an external plugin that provides the tool.
func (r *Router) findExternalPlugin(toolName, provider string) *ExternalPlugin {
	for _, ep := range r.external {
		if provider != "" && len(ep.ProvidesAI) > 0 {
			for _, p := range ep.ProvidesAI {
				if p == provider {
					for _, def := range ep.ToolDefs {
						if def.Name == toolName {
							return ep
						}
					}
				}
			}
		}
		for _, def := range ep.ToolDefs {
			if def.Name == toolName {
				return ep
			}
		}
	}
	return nil
}

// routeToolCall dispatches a tool call to the in-process handler or external plugin.
func (r *Router) routeToolCall(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.PluginResponse, error) {
	// Rate limit check. Use caller_plugin as the rate limit key; fall back to
	// "default" for callers that don't identify themselves.
	caller := req.GetCallerPlugin()
	if caller == "" {
		caller = "default"
	}
	if err := r.limiter.allow(caller); err != nil {
		slog.Warn("rate limit exceeded", "caller", caller, "tool", req.GetToolName(), "error", err)
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ToolCall{
				ToolCall: &pluginv1.ToolResponse{
					Success:      false,
					ErrorCode:    "rate_limited",
					ErrorMessage: err.Error(),
				},
			},
		}, nil
	}

	start := time.Now()
	toolName := req.GetToolName()
	inputSize := 0
	if req.GetArguments() != nil {
		inputSize = len(req.GetArguments().GetFields())
	}

	r.mu.RLock()
	handler, found := r.findToolHandler(toolName, req.GetProvider())
	ext := r.findExternalPlugin(toolName, req.GetProvider())
	r.mu.RUnlock()

	if found {
		result, err := safeCallTool(ctx, handler, req)
		isErr := err != nil || (result != nil && !result.Success)
		r.metrics.Record(toolName, time.Since(start), isErr, inputSize)
		if err != nil {
			return &pluginv1.PluginResponse{
				Response: &pluginv1.PluginResponse_ToolCall{
					ToolCall: &pluginv1.ToolResponse{
						Success:      false,
						ErrorCode:    "handler_error",
						ErrorMessage: err.Error(),
					},
				},
			}, nil
		}
		// Auto-publish event for mutating tool calls.
		if result != nil && result.Success {
			r.autoPublishToolEvent(toolName, req.GetArguments())
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ToolCall{ToolCall: result},
		}, nil
	}

	// Forward to external plugin via QUIC.
	if ext != nil {
		resp, err := ext.Send(ctx, &pluginv1.PluginRequest{
			RequestId: toolName,
			Request: &pluginv1.PluginRequest_ToolCall{
				ToolCall: req,
			},
		})
		isErr := err != nil
		r.metrics.Record(toolName, time.Since(start), isErr, inputSize)
		return resp, err
	}

	msg := fmt.Sprintf("no plugin provides tool %q", req.GetToolName())
	if req.GetProvider() != "" {
		msg = fmt.Sprintf("no plugin provides tool %q for provider %q", req.GetToolName(), req.GetProvider())
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_ToolCall{
			ToolCall: &pluginv1.ToolResponse{
				Success:      false,
				ErrorCode:    "tool_not_found",
				ErrorMessage: msg,
			},
		},
	}, nil
}

// listTools returns all registered tool definitions.
func (r *Router) listTools(_ context.Context) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var tools []*pluginv1.ToolDefinition
	for _, def := range r.toolDefs {
		tools = append(tools, def)
		seen[def.Name] = true
	}
	// Include AI tool defs from all providers.
	for _, providerDefs := range r.aiToolDefs {
		for _, def := range providerDefs {
			if !seen[def.Name] {
				tools = append(tools, def)
				seen[def.Name] = true
			}
		}
	}
	// Include external plugin tools not already in toolDefs.
	for _, ep := range r.external {
		for _, def := range ep.ToolDefs {
			if !seen[def.Name] {
				tools = append(tools, def)
				seen[def.Name] = true
			}
		}
	}

	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_ListTools{
			ListTools: &pluginv1.ListToolsResponse{Tools: tools},
		},
	}, nil
}

// listPrompts returns all registered prompt definitions.
func (r *Router) listPrompts(_ context.Context) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var prompts []*pluginv1.PromptDefinition
	for _, def := range r.promptDefs {
		prompts = append(prompts, def)
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_ListPrompts{
			ListPrompts: &pluginv1.ListPromptsResponse{Prompts: prompts},
		},
	}, nil
}

// routePromptGet dispatches to an in-process prompt handler or external plugin.
func (r *Router) routePromptGet(ctx context.Context, req *pluginv1.PromptGetRequest) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	handler, ok := r.promptHandlers[req.GetPromptName()]
	r.mu.RUnlock()

	if !ok {
		// Check external plugins.
		for _, ep := range r.external {
			for _, def := range ep.PromptDefs {
				if def.Name == req.GetPromptName() {
					return ep.Send(ctx, &pluginv1.PluginRequest{
						Request: &pluginv1.PluginRequest_PromptGet{PromptGet: req},
					})
				}
			}
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_PromptGet{
				PromptGet: &pluginv1.PromptGetResponse{
					Description: fmt.Sprintf("no plugin provides prompt %q", req.GetPromptName()),
				},
			},
		}, nil
	}

	result, err := safeCallPrompt(ctx, handler, req)
	if err != nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_PromptGet{
				PromptGet: &pluginv1.PromptGetResponse{
					Description: fmt.Sprintf("prompt handler error: %v", err),
				},
			},
		}, nil
	}

	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_PromptGet{PromptGet: result},
	}, nil
}

// GetMetrics returns the metrics collector for the router.
func (r *Router) GetMetrics() *Metrics {
	return r.metrics
}

// HealthCheck verifies that the router has storage and at least one tool registered.
func (r *Router) HealthCheck() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	toolCount := len(r.toolDefs)
	for _, providerDefs := range r.aiToolDefs {
		toolCount += len(providerDefs)
	}
	for _, ep := range r.external {
		toolCount += len(ep.ToolDefs)
	}

	status := "healthy"
	if toolCount == 0 {
		status = "degraded"
	}
	if r.storageHandler == nil {
		status = "degraded"
	}

	return map[string]any{
		"status":           status,
		"tools_registered": toolCount,
		"prompts_registered": len(r.promptDefs),
		"external_plugins": len(r.external),
		"has_storage":      r.storageHandler != nil,
	}
}

// GetStreamHandler returns the streaming tool handler for the given tool name.
func (r *Router) GetStreamHandler(toolName string) (plugin.StreamingToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.streamHandlers[toolName]
	return h, ok
}

// ListToolNames returns a list of all registered tool names (for logging).
func (r *Router) ListToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var names []string
	for name := range r.toolDefs {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, providerDefs := range r.aiToolDefs {
		for name := range providerDefs {
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	for _, ep := range r.external {
		for _, def := range ep.ToolDefs {
			if !seen[def.Name] {
				names = append(names, def.Name)
				seen[def.Name] = true
			}
		}
	}
	return names
}

// --- Panic recovery ---

// safeCallTool calls a tool handler with panic recovery. If the handler panics,
// the panic is caught and returned as an error instead of crashing the process.
func safeCallTool(ctx context.Context, handler plugin.ToolHandler, req *pluginv1.ToolRequest) (result *pluginv1.ToolResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in tool handler", "tool", req.GetToolName(), "panic", r)
			result = &pluginv1.ToolResponse{
				Success:      false,
				ErrorCode:    "panic",
				ErrorMessage: fmt.Sprintf("tool panicked: %v", r),
			}
			err = nil
		}
	}()
	return handler(ctx, req)
}

// safeCallPrompt calls a prompt handler with panic recovery.
func safeCallPrompt(ctx context.Context, handler plugin.PromptHandler, req *pluginv1.PromptGetRequest) (result *pluginv1.PromptGetResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in prompt handler", "prompt", req.GetPromptName(), "panic", r)
			result = &pluginv1.PromptGetResponse{
				Description: fmt.Sprintf("prompt panicked: %v", r),
			}
			err = nil
		}
	}()
	return handler(ctx, req)
}

// --- Storage routing ---

func (r *Router) routeStorageRead(ctx context.Context, req *pluginv1.StorageReadRequest) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	h := r.storageHandler
	r.mu.RUnlock()

	if h == nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageRead{
				StorageRead: &pluginv1.StorageReadResponse{},
			},
		}, nil
	}

	resp, err := h.Read(ctx, req)
	if err != nil {
		slog.Warn("storage read error", "path", req.GetPath(), "error", err)
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageRead{
				StorageRead: &pluginv1.StorageReadResponse{},
			},
		}, nil
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageRead{StorageRead: resp},
	}, nil
}

func (r *Router) routeStorageWrite(ctx context.Context, req *pluginv1.StorageWriteRequest) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	h := r.storageHandler
	r.mu.RUnlock()

	if h == nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageWrite{
				StorageWrite: &pluginv1.StorageWriteResponse{
					Success: false,
					Error:   "no storage handler registered",
				},
			},
		}, nil
	}

	resp, err := h.Write(ctx, req)
	if err != nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageWrite{
				StorageWrite: &pluginv1.StorageWriteResponse{
					Success: false,
					Error:   err.Error(),
				},
			},
		}, nil
	}
	if resp.GetSuccess() {
		r.autoPublishStorageEvent(req.GetPath(), "storage_write")
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageWrite{StorageWrite: resp},
	}, nil
}

func (r *Router) routeStorageDelete(ctx context.Context, req *pluginv1.StorageDeleteRequest) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	h := r.storageHandler
	r.mu.RUnlock()

	if h == nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageDelete{
				StorageDelete: &pluginv1.StorageDeleteResponse{Success: false},
			},
		}, nil
	}

	resp, err := h.Delete(ctx, req)
	if err != nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageDelete{
				StorageDelete: &pluginv1.StorageDeleteResponse{Success: false},
			},
		}, nil
	}
	if resp.GetSuccess() {
		r.autoPublishStorageEvent(req.GetPath(), "storage_delete")
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageDelete{StorageDelete: resp},
	}, nil
}

func (r *Router) routeStorageList(ctx context.Context, req *pluginv1.StorageListRequest) (*pluginv1.PluginResponse, error) {
	r.mu.RLock()
	h := r.storageHandler
	r.mu.RUnlock()

	if h == nil {
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageList{
				StorageList: &pluginv1.StorageListResponse{},
			},
		}, nil
	}

	resp, err := h.List(ctx, req)
	if err != nil {
		slog.Warn("storage list error", "prefix", req.GetPrefix(), "error", err)
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageList{
				StorageList: &pluginv1.StorageListResponse{},
			},
		}, nil
	}
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageList{StorageList: resp},
	}, nil
}

// --- Tool Catalog ---

// ListCatalog returns all registered tools as CatalogEntry values, sorted by name.
// If pluginFilter is non-empty, only tools from that plugin are returned.
func (r *Router) ListCatalog(pluginFilter string, offset, limit int) []CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := r.buildCatalog(pluginFilter)

	// Apply pagination.
	if offset >= len(entries) {
		return nil
	}
	end := offset + limit
	if end > len(entries) || limit <= 0 {
		end = len(entries)
	}
	return entries[offset:end]
}

// CatalogCount returns the total number of catalog entries (optionally filtered by plugin).
func (r *Router) CatalogCount(pluginFilter string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.buildCatalog(pluginFilter))
}

// SearchCatalog searches tool names and descriptions for the query string.
// Results are sorted: exact name matches first, then name-contains, then description-contains.
func (r *Router) SearchCatalog(query string) []CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := r.buildCatalog("")
	q := strings.ToLower(query)

	var exact, nameMatch, descMatch []CatalogEntry
	for _, e := range all {
		nameLower := strings.ToLower(e.Name)
		if nameLower == q {
			exact = append(exact, e)
		} else if strings.Contains(nameLower, q) {
			nameMatch = append(nameMatch, e)
		} else if strings.Contains(strings.ToLower(e.Description), q) {
			descMatch = append(descMatch, e)
		}
	}

	result := make([]CatalogEntry, 0, len(exact)+len(nameMatch)+len(descMatch))
	result = append(result, exact...)
	result = append(result, nameMatch...)
	result = append(result, descMatch...)
	return result
}

// GetCatalogEntry returns a single tool's CatalogEntry by name, or nil if not found.
func (r *Router) GetCatalogEntry(toolName string) *CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def := r.findToolDef(toolName)
	if def == nil {
		return nil
	}

	entry := CatalogEntry{
		Name:        def.Name,
		Description: def.Description,
		PluginID:    r.toolPluginMap[toolName],
		Category:    r.toolCategoryMap[toolName],
		Providers:   r.toolProvidersMap[toolName],
	}

	// Serialize schema to JSON for the detail view.
	if def.InputSchema != nil {
		b, err := protojson.Marshal(def.InputSchema)
		if err == nil {
			entry.Schema = string(b)
		}
	}

	return &entry
}

// ListPluginIDs returns the unique set of plugin IDs that have registered tools.
func (r *Router) ListPluginIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	for _, pid := range r.toolPluginMap {
		seen[pid] = true
	}
	ids := make([]string, 0, len(seen))
	for pid := range seen {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	return ids
}

// buildCatalog assembles all catalog entries, optionally filtered by plugin ID.
// Must be called with r.mu held (at least RLock).
func (r *Router) buildCatalog(pluginFilter string) []CatalogEntry {
	seen := make(map[string]bool)
	var entries []CatalogEntry

	addDef := func(def *pluginv1.ToolDefinition) {
		if seen[def.Name] {
			return
		}
		if pluginFilter != "" && r.toolPluginMap[def.Name] != pluginFilter {
			return
		}
		seen[def.Name] = true
		entries = append(entries, CatalogEntry{
			Name:        def.Name,
			Description: def.Description,
			PluginID:    r.toolPluginMap[def.Name],
			Category:    r.toolCategoryMap[def.Name],
			Providers:   r.toolProvidersMap[def.Name],
		})
	}

	for _, def := range r.toolDefs {
		addDef(def)
	}
	for _, providerDefs := range r.aiToolDefs {
		for _, def := range providerDefs {
			addDef(def)
		}
	}
	for _, ep := range r.external {
		for _, def := range ep.ToolDefs {
			addDef(def)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

// findToolDef looks up a ToolDefinition by name across all maps.
// Must be called with r.mu held (at least RLock).
func (r *Router) findToolDef(toolName string) *pluginv1.ToolDefinition {
	if def, ok := r.toolDefs[toolName]; ok {
		return def
	}
	for _, providerDefs := range r.aiToolDefs {
		if def, ok := providerDefs[toolName]; ok {
			return def
		}
	}
	for _, ep := range r.external {
		for _, def := range ep.ToolDefs {
			if def.Name == toolName {
				return def
			}
		}
	}
	return nil
}

// toolTopicMap maps tool names to event topics for auto-publishing after
// successful mutating tool calls.
var toolTopicMap = map[string]string{
	"create_feature":      "features",
	"advance_feature":     "features",
	"update_feature":      "features",
	"submit_review":       "features",
	"set_current_feature": "features",
	"reject_feature":      "features",
	"delete_feature":      "features",
	"unlock_feature":      "features",
	"assign_feature":      "features",
	"unassign_feature":    "features",
	"bulk_assign_features": "features",

	"create_project": "projects",
	"update_project": "projects",
	"delete_project": "projects",

	"create_plan":    "plans",
	"update_plan":    "plans",
	"approve_plan":   "plans",
	"complete_plan":  "plans",
	"breakdown_plan": "plans",
	"delete_plan":    "plans",

	"create_note":       "notes",
	"update_note":       "notes",
	"delete_note":       "notes",
	"pin_note":          "notes",
	"tag_note":          "notes",
	"save_feature_note": "notes",

	"create_person": "persons",
	"update_person": "persons",
	"delete_person": "persons",

	"delegate_feature":     "delegations",
	"respond_delegation":   "delegations",

	"create_request": "requests",
	"convert_request": "requests",
	"dismiss_request": "requests",

	"doc_create":   "docs",
	"doc_update":   "docs",
	"doc_delete":   "docs",
	"doc_generate": "docs",

	"create_agent": "agents",
	"update_agent": "agents",
	"delete_agent": "agents",

	"create_skill": "skills",
	"update_skill": "skills",
	"delete_skill": "skills",

	"create_workflow": "workflows",
	"update_workflow": "workflows",
	"delete_workflow": "workflows",

	"create_hook":        "hooks",
	"update_hook":        "hooks",
	"delete_hook":        "hooks",
	"receive_hook_event": "hooks",

	"create_prompt": "prompts",
	"update_prompt": "prompts",
	"delete_prompt": "prompts",

	"create_action": "actions",
	"update_action": "actions",
	"delete_action": "actions",

	"create_experiment":      "experiments",
	"update_experiment":      "experiments",
	"start_experiment":       "experiments",
	"complete_experiment":    "experiments",
	"abandon_experiment":     "experiments",

	"create_hypothesis":    "hypotheses",
	"update_hypothesis":    "hypotheses",
	"refine_hypothesis":    "hypotheses",
	"validate_hypothesis":  "hypotheses",
	"invalidate_hypothesis": "hypotheses",
}

// autoPublishToolEvent publishes an event to the EventBus after a successful
// mutating tool call. Only tools in toolTopicMap trigger events.
func (r *Router) autoPublishToolEvent(toolName string, args *structpb.Struct) {
	topic, ok := toolTopicMap[toolName]
	if !ok {
		return
	}
	r.eventBus.Publish(topic, toolName, args, "router")
}

// storagePathTopicMap maps storage path prefixes to event topics.
var storagePathTopicMap = [][2]string{
	{"orchestra-agents/features/", "features"},
	{"orchestra-agents/plans/", "plans"},
	{"orchestra-agents/requests/", "requests"},
	{"orchestra-agents/delegations/", "delegations"},
	{"orchestra-agents/persons/", "persons"},
	{".notes/", "notes"},
	{".projects/", "projects"},
	{"docs/", "docs"},
	{"agents/agents/", "agents"},
	{"agents/workflows/", "workflows"},
	{".skills/", "skills"},
	{".hooks/", "hooks"},
	{".prompts/", "prompts"},
	{".actions/", "actions"},
}

// autoPublishStorageEvent publishes an event to the EventBus after a successful
// storage write or delete. The topic is derived from the storage path prefix.
func (r *Router) autoPublishStorageEvent(path, eventType string) {
	for _, entry := range storagePathTopicMap {
		if strings.Contains(path, entry[0]) {
			payload, _ := structpb.NewStruct(map[string]any{"path": path})
			r.eventBus.Publish(entry[1], eventType, payload, "storage")
			return
		}
	}
}
