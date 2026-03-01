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

// ---------- define_workflow ----------

// DefineWorkflowSchema returns the JSON Schema for the define_workflow tool.
func DefineWorkflowSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Workflow ID (auto-generated if empty)",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Display name for the workflow",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "Workflow type: sequential, parallel, or loop",
				"enum":        []any{"sequential", "parallel", "loop"},
			},
			"steps": map[string]any{
				"type":        "string",
				"description": "JSON array of step objects: [{\"agent_id\":\"...\", \"provider\":\"...\", \"model\":\"...\"}]",
			},
			"max_budget": map[string]any{
				"type":        "number",
				"description": "Total budget cap in USD for the entire workflow",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Workflow description/documentation",
			},
		},
		"required": []any{"name", "type", "steps"},
	})
	return s
}

// DefineWorkflow creates or updates a multi-agent workflow definition.
func DefineWorkflow(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "name", "type", "steps"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		id := helpers.GetString(req.Arguments, "id")
		name := helpers.GetString(req.Arguments, "name")
		wfType := helpers.GetString(req.Arguments, "type")
		steps := helpers.GetString(req.Arguments, "steps")
		maxBudget := helpers.GetFloat64(req.Arguments, "max_budget")
		description := helpers.GetString(req.Arguments, "description")

		if err := helpers.ValidateOneOf(wfType, "sequential", "parallel", "loop"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		now := helpers.NowISO()
		var expectedVersion int64

		if id != "" {
			existing, version, err := deps.Storage.ReadWorkflow(ctx, id)
			if err == nil {
				expectedVersion = version
				if existing.CreatedAt != "" {
					now = existing.CreatedAt
				}
			}
		} else {
			id = newWorkflowID()
		}

		wf := &storage.Workflow{
			ID:          id,
			Name:        name,
			Type:        wfType,
			Steps:       steps,
			MaxBudget:   maxBudget,
			Description: description,
			CreatedAt:   now,
			UpdatedAt:   helpers.NowISO(),
		}

		_, err := deps.Storage.WriteWorkflow(ctx, wf, expectedVersion)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to save workflow: %v", err)), nil
		}

		md := formatWorkflowMD(wf)
		return helpers.TextResult(md), nil
	}
}

// ---------- get_workflow ----------

// GetWorkflowSchema returns the JSON Schema for the get_workflow tool.
func GetWorkflowSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workflow_id": map[string]any{
				"type":        "string",
				"description": "Workflow ID to retrieve",
			},
		},
		"required": []any{"workflow_id"},
	})
	return s
}

// GetWorkflow retrieves a workflow definition by ID.
func GetWorkflow(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "workflow_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		workflowID := helpers.GetString(req.Arguments, "workflow_id")
		wf, _, err := deps.Storage.ReadWorkflow(ctx, workflowID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("workflow %q not found", workflowID)), nil
		}

		md := formatWorkflowMD(wf)
		return helpers.TextResult(md), nil
	}
}

// ---------- list_workflows ----------

// ListWorkflowsSchema returns the JSON Schema for the list_workflows tool.
func ListWorkflowsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return s
}

// ListWorkflows returns all defined workflows.
func ListWorkflows(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		workflows, err := deps.Storage.ListWorkflows(ctx)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to list workflows: %v", err)), nil
		}

		if len(workflows) == 0 {
			return helpers.TextResult("## Workflows\n\nNo workflows defined.\n"), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## Workflows (%d)\n\n", len(workflows))
		fmt.Fprintf(&b, "| ID | Name | Type | Budget |\n")
		fmt.Fprintf(&b, "|----|------|------|--------|\n")
		for _, wf := range workflows {
			budget := "unlimited"
			if wf.MaxBudget > 0 {
				budget = fmt.Sprintf("$%.2f", wf.MaxBudget)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", wf.ID, wf.Name, wf.Type, budget)
		}
		return helpers.TextResult(b.String()), nil
	}
}

// ---------- delete_workflow ----------

// DeleteWorkflowSchema returns the JSON Schema for the delete_workflow tool.
func DeleteWorkflowSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workflow_id": map[string]any{
				"type":        "string",
				"description": "Workflow ID to delete",
			},
		},
		"required": []any{"workflow_id"},
	})
	return s
}

// DeleteWorkflow removes a workflow definition by ID.
func DeleteWorkflow(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "workflow_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		workflowID := helpers.GetString(req.Arguments, "workflow_id")

		_, _, err := deps.Storage.ReadWorkflow(ctx, workflowID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("workflow %q not found", workflowID)), nil
		}

		if err := deps.Storage.DeleteWorkflow(ctx, workflowID); err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to delete workflow: %v", err)), nil
		}

		return helpers.TextResult(fmt.Sprintf("Workflow %q deleted successfully.", workflowID)), nil
	}
}

// ---------- Helpers ----------

func newWorkflowID() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 4)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return "WFL-" + string(b)
}

func formatWorkflowMD(wf *storage.Workflow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Workflow: %s\n\n", wf.Name)
	fmt.Fprintf(&b, "- **ID:** %s\n", wf.ID)
	fmt.Fprintf(&b, "- **Type:** %s\n", wf.Type)
	if wf.MaxBudget > 0 {
		fmt.Fprintf(&b, "- **Max Budget:** $%.2f\n", wf.MaxBudget)
	}
	fmt.Fprintf(&b, "- **Created:** %s\n", wf.CreatedAt)
	fmt.Fprintf(&b, "- **Updated:** %s\n", wf.UpdatedAt)
	if wf.Steps != "" {
		fmt.Fprintf(&b, "\n#### Steps\n\n```json\n%s\n```\n", wf.Steps)
	}
	if wf.Description != "" {
		fmt.Fprintf(&b, "\n#### Description\n\n%s\n", wf.Description)
	}
	return b.String()
}
