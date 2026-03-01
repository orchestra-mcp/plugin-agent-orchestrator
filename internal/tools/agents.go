package tools

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// ---------- define_agent ----------

// DefineAgentSchema returns the JSON Schema for the define_agent tool.
func DefineAgentSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Agent ID (auto-generated if empty)",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Display name for the agent",
			},
			"instruction": map[string]any{
				"type":        "string",
				"description": "System instruction/prompt for the agent",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "AI provider: claude, openai, gemini, ollama, deepseek, qwen, kimi, grok, perplexity",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Model name (e.g. claude-sonnet-4-20250514, gpt-4o)",
			},
			"account_id": map[string]any{
				"type":        "string",
				"description": "Account ID for credentials (from agentops)",
			},
			"tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "MCP tool names the agent can use",
			},
			"max_budget": map[string]any{
				"type":        "number",
				"description": "Maximum budget in USD per run",
			},
			"output_key": map[string]any{
				"type":        "string",
				"description": "Key to store the agent's output in workflow state",
			},
		},
		"required": []any{"name", "instruction"},
	})
	return s
}

// DefineAgent creates or updates an AI agent definition.
func DefineAgent(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "name", "instruction"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		id := helpers.GetString(req.Arguments, "id")
		name := helpers.GetString(req.Arguments, "name")
		instruction := helpers.GetString(req.Arguments, "instruction")
		provider := helpers.GetStringOr(req.Arguments, "provider", "claude")
		model := helpers.GetString(req.Arguments, "model")
		accountID := helpers.GetString(req.Arguments, "account_id")
		tools := helpers.GetStringSlice(req.Arguments, "tools")
		maxBudget := helpers.GetFloat64(req.Arguments, "max_budget")
		outputKey := helpers.GetString(req.Arguments, "output_key")

		now := helpers.NowISO()
		var expectedVersion int64

		if id != "" {
			// Try to update existing agent.
			existing, version, err := deps.Storage.ReadAgent(ctx, id)
			if err == nil {
				expectedVersion = version
				if existing.CreatedAt != "" {
					now = existing.CreatedAt // preserve original created_at
				}
			}
		} else {
			id = newAgentID()
		}

		agent := &storage.Agent{
			ID:          id,
			Name:        name,
			Provider:    provider,
			Model:       model,
			AccountID:   accountID,
			Tools:       tools,
			MaxBudget:   maxBudget,
			OutputKey:   outputKey,
			Instruction: instruction,
			CreatedAt:   now,
			UpdatedAt:   helpers.NowISO(),
		}

		_, err := deps.Storage.WriteAgent(ctx, agent, expectedVersion)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to save agent: %v", err)), nil
		}

		md := formatAgentMD(agent)
		return helpers.TextResult(md), nil
	}
}

// ---------- get_agent ----------

// GetAgentSchema returns the JSON Schema for the get_agent tool.
func GetAgentSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Agent ID to retrieve",
			},
		},
		"required": []any{"agent_id"},
	})
	return s
}

// GetAgent retrieves an agent definition by ID.
func GetAgent(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "agent_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		agentID := helpers.GetString(req.Arguments, "agent_id")
		agent, _, err := deps.Storage.ReadAgent(ctx, agentID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("agent %q not found", agentID)), nil
		}

		md := formatAgentMD(agent)
		return helpers.TextResult(md), nil
	}
}

// ---------- list_agents ----------

// ListAgentsSchema returns the JSON Schema for the list_agents tool.
func ListAgentsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return s
}

// ListAgents returns all defined agents.
func ListAgents(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		agents, err := deps.Storage.ListAgents(ctx)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to list agents: %v", err)), nil
		}

		if len(agents) == 0 {
			return helpers.TextResult("## Agents\n\nNo agents defined.\n"), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## Agents (%d)\n\n", len(agents))
		fmt.Fprintf(&b, "| ID | Name | Provider | Model |\n")
		fmt.Fprintf(&b, "|----|------|----------|-------|\n")
		for _, a := range agents {
			model := a.Model
			if model == "" {
				model = "default"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", a.ID, a.Name, a.Provider, model)
		}
		return helpers.TextResult(b.String()), nil
	}
}

// ---------- delete_agent ----------

// DeleteAgentSchema returns the JSON Schema for the delete_agent tool.
func DeleteAgentSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Agent ID to delete",
			},
		},
		"required": []any{"agent_id"},
	})
	return s
}

// DeleteAgent removes an agent definition by ID.
func DeleteAgent(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "agent_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		agentID := helpers.GetString(req.Arguments, "agent_id")

		// Verify it exists first.
		_, _, err := deps.Storage.ReadAgent(ctx, agentID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("agent %q not found", agentID)), nil
		}

		if err := deps.Storage.DeleteAgent(ctx, agentID); err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to delete agent: %v", err)), nil
		}

		return helpers.TextResult(fmt.Sprintf("Agent %q deleted successfully.", agentID)), nil
	}
}

// ---------- Helpers ----------

func newAgentID() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 4)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "AGT-" + string(b)
}

func formatAgentMD(a *storage.Agent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Agent: %s\n\n", a.Name)
	fmt.Fprintf(&b, "- **ID:** %s\n", a.ID)
	fmt.Fprintf(&b, "- **Provider:** %s\n", a.Provider)
	if a.Model != "" {
		fmt.Fprintf(&b, "- **Model:** %s\n", a.Model)
	}
	if a.AccountID != "" {
		fmt.Fprintf(&b, "- **Account:** %s\n", a.AccountID)
	}
	if len(a.Tools) > 0 {
		fmt.Fprintf(&b, "- **Tools:** %s\n", strings.Join(a.Tools, ", "))
	}
	if a.MaxBudget > 0 {
		fmt.Fprintf(&b, "- **Max Budget:** $%.2f\n", a.MaxBudget)
	}
	if a.OutputKey != "" {
		fmt.Fprintf(&b, "- **Output Key:** %s\n", a.OutputKey)
	}
	fmt.Fprintf(&b, "- **Created:** %s\n", a.CreatedAt)
	fmt.Fprintf(&b, "- **Updated:** %s\n", a.UpdatedAt)
	if a.Instruction != "" {
		fmt.Fprintf(&b, "\n#### Instruction\n\n%s\n", a.Instruction)
	}
	return b.String()
}
