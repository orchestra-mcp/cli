package inprocess

import (
	"context"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
)

// ExternalPlugin represents a QUIC-connected plugin that runs as a separate
// process (e.g. engine-rag written in Rust, or third-party plugins).
// The router forwards requests to it via its Send method.
type ExternalPlugin struct {
	ID         string
	ProvidesAI []string
	ToolDefs   []*pluginv1.ToolDefinition
	PromptDefs []*pluginv1.PromptDefinition
	Client     Sender
}

// Sender abstracts the QUIC client for external plugins.
type Sender interface {
	Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

// Send forwards a request to the external plugin via its QUIC client.
func (ep *ExternalPlugin) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	return ep.Client.Send(ctx, req)
}
