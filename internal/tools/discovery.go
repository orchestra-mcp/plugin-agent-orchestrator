package tools

import (
	"context"
	"fmt"
	"strings"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"google.golang.org/protobuf/types/known/structpb"
)

// knownModels maps each AI provider to its known models.
var knownModels = map[string][]string{
	"claude": {
		"claude-opus-4-20250514",
		"claude-sonnet-4-20250514",
		"claude-haiku-4-5-20251001",
	},
	"openai": {
		"gpt-4o",
		"gpt-4o-mini",
		"o1",
		"o3-mini",
	},
	"gemini": {
		"gemini-2.0-flash",
		"gemini-1.5-pro",
	},
	"ollama": {
		"llama3",
		"mistral",
		"codellama",
	},
	"deepseek": {
		"deepseek-chat",
		"deepseek-reasoner",
	},
	"grok": {
		"grok-2",
		"grok-2-mini",
	},
	"qwen": {
		"qwen-max",
		"qwen-plus",
		"qwen-turbo",
	},
	"kimi": {
		"moonshot-v1-8k",
		"moonshot-v1-32k",
		"moonshot-v1-128k",
	},
	"perplexity": {
		"sonar",
		"sonar-pro",
	},
}

// ListAvailableModelsSchema returns the JSON Schema for the list_available_models tool.
func ListAvailableModelsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider": map[string]any{
				"type":        "string",
				"description": "Filter by provider name (optional). If empty, lists all providers.",
			},
		},
	})
	return s
}

// ListAvailableModels returns a static list of known models per provider.
func ListAvailableModels(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		_ = deps // not used for discovery but kept for interface consistency

		provider := helpers.GetString(req.Arguments, "provider")

		var b strings.Builder
		fmt.Fprintf(&b, "## Available Models\n\n")

		if provider != "" {
			models, ok := knownModels[provider]
			if !ok {
				return helpers.ErrorResult("not_found",
					fmt.Sprintf("unknown provider %q", provider)), nil
			}
			fmt.Fprintf(&b, "### %s\n\n", provider)
			for _, m := range models {
				fmt.Fprintf(&b, "- `%s`\n", m)
			}
			if provider == "ollama" {
				fmt.Fprintf(&b, "\n_Note: Actual models depend on local Ollama installation._\n")
			}
		} else {
			// List all providers and their models in a consistent order.
			providerOrder := []string{
				"claude", "openai", "gemini", "ollama", "deepseek",
				"grok", "qwen", "kimi", "perplexity",
			}
			for _, p := range providerOrder {
				models := knownModels[p]
				fmt.Fprintf(&b, "### %s\n\n", p)
				for _, m := range models {
					fmt.Fprintf(&b, "- `%s`\n", m)
				}
				if p == "ollama" {
					fmt.Fprintf(&b, "\n_Note: Actual models depend on local Ollama installation._\n")
				}
				fmt.Fprintf(&b, "\n")
			}
		}

		return helpers.TextResult(b.String()), nil
	}
}
