// Package inprocess provides an in-process plugin router that dispatches tool
// calls, storage operations, and prompt requests via direct Go function calls
// instead of QUIC network round-trips. It implements the Sender interface used
// by StdioTransport and the StorageClient interface used by plugins.
package inprocess

import (
	"context"
	"fmt"
	"log"
	"sync"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

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
}

// NewRouter creates a new in-process Router.
func NewRouter() *Router {
	return &Router{
		toolHandlers:   make(map[string]plugin.ToolHandler),
		streamHandlers: make(map[string]plugin.StreamingToolHandler),
		promptHandlers: make(map[string]plugin.PromptHandler),
		toolDefs:       make(map[string]*pluginv1.ToolDefinition),
		promptDefs:     make(map[string]*pluginv1.PromptDefinition),
		aiToolHandlers: make(map[string]map[string]plugin.ToolHandler),
		aiToolDefs:     make(map[string]map[string]*pluginv1.ToolDefinition),
		external:       make(map[string]*ExternalPlugin),
	}
}

// SetStorageHandler sets the storage backend (typically storage-markdown).
func (r *Router) SetStorageHandler(h plugin.StorageHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storageHandler = h
}

// RegisterPlugin registers all tools, streaming tools, and prompts from an
// ExportedPlugin. If the plugin's manifest declares ProvidesAI, tools are
// indexed under the AI routing table instead of the generic tool table.
func (r *Router) RegisterPlugin(ep *plugin.ExportedPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	isAI := len(ep.Manifest.GetProvidesAi()) > 0

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
		} else {
			r.toolHandlers[t.Name] = t.Handler
			r.toolDefs[t.Name] = def
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
		} else {
			r.toolDefs[st.Name] = def
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

	log.Printf("[inprocess] registered plugin %q (%d tools, %d prompts)", ep.ID, len(ep.Tools)+len(ep.StreamTools), len(ep.Prompts))
}

// RegisterExternal adds an external QUIC-connected plugin to the router.
func (r *Router) RegisterExternal(ep *ExternalPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.external[ep.ID] = ep

	for _, def := range ep.ToolDefs {
		if len(ep.ProvidesAI) > 0 {
			for _, provider := range ep.ProvidesAI {
				if r.aiToolDefs[provider] == nil {
					r.aiToolDefs[provider] = make(map[string]*pluginv1.ToolDefinition)
				}
				r.aiToolDefs[provider][def.Name] = def
			}
		} else {
			r.toolDefs[def.Name] = def
		}
	}

	for _, def := range ep.PromptDefs {
		r.promptDefs[def.Name] = def
	}

	log.Printf("[inprocess] registered external plugin %q (%d tools)", ep.ID, len(ep.ToolDefs))
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
		// Events are not yet implemented in-process; acknowledge silently.
		return &pluginv1.PluginResponse{RequestId: req.GetRequestId()}, nil

	case *pluginv1.PluginRequest_Subscribe:
		return &pluginv1.PluginResponse{RequestId: req.GetRequestId()}, nil

	case *pluginv1.PluginRequest_Unsubscribe:
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
	r.mu.RLock()
	handler, found := r.findToolHandler(req.GetToolName(), req.GetProvider())
	ext := r.findExternalPlugin(req.GetToolName(), req.GetProvider())
	r.mu.RUnlock()

	if found {
		result, err := handler(ctx, req)
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
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ToolCall{ToolCall: result},
		}, nil
	}

	// Forward to external plugin via QUIC.
	if ext != nil {
		return ext.Send(ctx, &pluginv1.PluginRequest{
			RequestId: req.GetToolName(),
			Request: &pluginv1.PluginRequest_ToolCall{
				ToolCall: req,
			},
		})
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

	var tools []*pluginv1.ToolDefinition
	for _, def := range r.toolDefs {
		tools = append(tools, def)
	}
	// Include AI tool defs from all providers.
	seen := make(map[string]bool)
	for _, providerDefs := range r.aiToolDefs {
		for _, def := range providerDefs {
			if !seen[def.Name] {
				tools = append(tools, def)
				seen[def.Name] = true
			}
		}
	}
	// Include external plugin tools.
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

	result, err := handler(ctx, req)
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
		log.Printf("[inprocess] storage_read error: %v", err)
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
		log.Printf("[inprocess] storage_list error: %v", err)
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
