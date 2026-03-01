package tools

import (
	"context"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
)

// ToolHandler is the function signature for MCP tool handlers.
type ToolHandler = func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error)

// ToolDeps holds shared dependencies injected into all tool handlers.
type ToolDeps struct {
	Storage *storage.DataStorage
}
