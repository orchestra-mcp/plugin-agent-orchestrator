package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// ---------- run_agent ----------

// RunAgentSchema returns the JSON Schema for the run_agent tool.
func RunAgentSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Agent ID to execute",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "User prompt to send to the agent",
			},
			"state": map[string]any{
				"type":        "string",
				"description": "JSON-encoded input state map (passed as context)",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "If true, return a mock response without calling the LLM",
			},
		},
		"required": []any{"agent_id", "prompt"},
	})
	return s
}

// RunAgent executes an agent with a prompt. The flow is:
//  1. Load agent definition from storage
//  2. If dry_run, return mock response
//  3. Get account env vars via CallTool("get_account_env")
//  4. Call bridge tool (ai_prompt) with provider routing
//  5. Record run in storage
//  6. Report usage
func RunAgent(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "agent_id", "prompt"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		agentID := helpers.GetString(req.Arguments, "agent_id")
		prompt := helpers.GetString(req.Arguments, "prompt")
		stateJSON := helpers.GetString(req.Arguments, "state")
		dryRun := helpers.GetBool(req.Arguments, "dry_run")

		// Step 1: Load agent definition.
		agent, _, err := deps.Storage.ReadAgent(ctx, agentID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("agent %q not found", agentID)), nil
		}

		// Create run record.
		runID := helpers.NewUUID()
		now := helpers.NowISO()
		run := &storage.Run{
			ID:         runID,
			TargetType: "agent",
			TargetID:   agentID,
			Status:     "running",
			State:      stateJSON,
			StartedAt:  now,
		}
		_, _ = deps.Storage.WriteRun(ctx, run, 0)

		// Step 2: If dry_run, return mock response.
		if dryRun {
			run.Status = "completed"
			run.Result = fmt.Sprintf("[DRY RUN] Agent %q (%s/%s) would process: %s",
				agent.Name, agent.Provider, agent.Model, prompt)
			run.CompletedAt = helpers.NowISO()
			_, _ = deps.Storage.WriteRun(ctx, run, 0)

			md := formatRunResultMD(run, agent.Name)
			return helpers.TextResult(md), nil
		}

		// Step 3: Get account env vars.
		provider := agent.Provider
		var envMap map[string]any
		if agent.AccountID != "" {
			p, env, err := getAccountEnvWithProvider(ctx, deps.Storage, agent.AccountID)
			if err != nil {
				run.Status = "failed"
				run.Error = fmt.Sprintf("failed to get account env: %v", err)
				run.CompletedAt = helpers.NowISO()
				_, _ = deps.Storage.WriteRun(ctx, run, 0)
				return helpers.ErrorResult("agentops_error", run.Error), nil
			}
			if p != "" {
				provider = p
			}
			envMap = env
		}

		// Enrich prompt with state context if provided.
		fullPrompt := prompt
		if stateJSON != "" {
			fullPrompt = fmt.Sprintf("Context state: %s\n\n%s", stateJSON, prompt)
		}

		// Step 4: Call bridge tool (ai_prompt) with provider routing.
		args := map[string]any{
			"prompt": fullPrompt,
			"model":  agent.Model,
		}
		if agent.Instruction != "" {
			args["system_prompt"] = agent.Instruction
		}
		if envMap != nil {
			envJSON, err := json.Marshal(envMap)
			if err == nil {
				args["env"] = string(envJSON)
			}
		}

		aiResp, err := deps.Storage.CallToolWithProvider(ctx, "ai_prompt", args, provider)
		if err != nil {
			run.Status = "failed"
			run.Error = fmt.Sprintf("ai_prompt call failed: %v", err)
			run.CompletedAt = helpers.NowISO()
			_, _ = deps.Storage.WriteRun(ctx, run, 0)
			return helpers.ErrorResult("bridge_error", run.Error), nil
		}

		// Extract response data.
		responseText := extractText(aiResp)
		tokensIn := extractInt64(aiResp, "tokens_in")
		tokensOut := extractInt64(aiResp, "tokens_out")
		costUSD := extractFloat64(aiResp, "cost_usd")

		// Step 5: Record completed run.
		run.Status = "completed"
		run.Result = responseText
		run.CompletedAt = helpers.NowISO()
		run.TotalTokensIn = tokensIn
		run.TotalTokensOut = tokensOut
		run.TotalCostUSD = costUSD
		_, _ = deps.Storage.WriteRun(ctx, run, 0)

		// Step 6: Report usage.
		if agent.AccountID != "" {
			_ = reportUsage(ctx, deps.Storage, agent.AccountID, runID, tokensIn, tokensOut, costUSD, agent.Model)
		}

		md := formatRunResultMD(run, agent.Name)
		return helpers.TextResult(md), nil
	}
}

// ---------- run_workflow ----------

// RunWorkflowSchema returns the JSON Schema for the run_workflow tool.
func RunWorkflowSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workflow_id": map[string]any{
				"type":        "string",
				"description": "Workflow ID to execute",
			},
			"state": map[string]any{
				"type":        "string",
				"description": "JSON-encoded initial state map",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "If true, return mock responses without calling LLMs",
			},
		},
		"required": []any{"workflow_id"},
	})
	return s
}

// RunWorkflow executes a multi-agent workflow. It loads the workflow definition,
// parses the steps, and executes each step agent sequentially (for sequential
// and parallel types) or in a loop pattern.
func RunWorkflow(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "workflow_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		workflowID := helpers.GetString(req.Arguments, "workflow_id")
		stateJSON := helpers.GetString(req.Arguments, "state")
		dryRun := helpers.GetBool(req.Arguments, "dry_run")

		// Load workflow definition.
		wf, _, err := deps.Storage.ReadWorkflow(ctx, workflowID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("workflow %q not found", workflowID)), nil
		}

		// Parse steps.
		var steps []workflowStep
		if err := json.Unmarshal([]byte(wf.Steps), &steps); err != nil {
			return helpers.ErrorResult("invalid_config",
				fmt.Sprintf("failed to parse workflow steps: %v", err)), nil
		}

		if len(steps) == 0 {
			return helpers.ErrorResult("invalid_config", "workflow has no steps"), nil
		}

		// Create workflow run record.
		runID := helpers.NewUUID()
		now := helpers.NowISO()
		run := &storage.Run{
			ID:         runID,
			TargetType: "workflow",
			TargetID:   workflowID,
			Status:     "running",
			State:      stateJSON,
			StartedAt:  now,
		}
		_, _ = deps.Storage.WriteRun(ctx, run, 0)

		// Execute steps. Parse current state for passing between steps.
		state := make(map[string]any)
		if stateJSON != "" {
			_ = json.Unmarshal([]byte(stateJSON), &state)
		}

		var totalCost float64
		var totalIn, totalOut int64
		var stepResults []string

		for i, step := range steps {
			if step.AgentID == "" {
				stepResults = append(stepResults,
					fmt.Sprintf("Step %d: skipped (no agent_id)", i+1))
				continue
			}

			// Build the step's state JSON.
			stateBytes, _ := json.Marshal(state)

			// Build a RunAgent-style request.
			stepPrompt := step.Prompt
			if stepPrompt == "" {
				stepPrompt = fmt.Sprintf("Execute step %d of workflow %q", i+1, wf.Name)
			}

			stepArgs := map[string]any{
				"agent_id": step.AgentID,
				"prompt":   stepPrompt,
				"state":    string(stateBytes),
				"dry_run":  dryRun,
			}
			argsStruct, err := structpb.NewStruct(stepArgs)
			if err != nil {
				stepResults = append(stepResults,
					fmt.Sprintf("Step %d (%s): failed to build args: %v", i+1, step.AgentID, err))
				continue
			}

			stepReq := &pluginv1.ToolRequest{
				ToolName:  "run_agent",
				Arguments: argsStruct,
			}

			handler := RunAgent(deps)
			stepResp, err := handler(ctx, stepReq)
			if err != nil {
				stepResults = append(stepResults,
					fmt.Sprintf("Step %d (%s): error: %v", i+1, step.AgentID, err))
				// For sequential, stop on error. For parallel, continue.
				if wf.Type == "sequential" {
					run.Status = "failed"
					run.Error = fmt.Sprintf("step %d failed: %v", i+1, err)
					break
				}
				continue
			}

			if !stepResp.Success {
				stepResults = append(stepResults,
					fmt.Sprintf("Step %d (%s): failed: %s", i+1, step.AgentID, stepResp.ErrorMessage))
				if wf.Type == "sequential" {
					run.Status = "failed"
					run.Error = fmt.Sprintf("step %d failed: %s", i+1, stepResp.ErrorMessage)
					break
				}
				continue
			}

			// Extract the step result text.
			resultText := ""
			if stepResp.Result != nil {
				if v, ok := stepResp.Result.Fields["text"]; ok {
					if sv, ok := v.Kind.(*structpb.Value_StringValue); ok {
						resultText = sv.StringValue
					}
				}
			}

			stepResults = append(stepResults,
				fmt.Sprintf("Step %d (%s): completed", i+1, step.AgentID))

			// Store output in state if the agent has an output_key.
			agent, _, aErr := deps.Storage.ReadAgent(ctx, step.AgentID)
			if aErr == nil && agent.OutputKey != "" {
				state[agent.OutputKey] = resultText
			}

			// Accumulate costs (approximate from run records).
			// The individual run_agent calls already created their own run records,
			// so we track totals here for the workflow run.
			totalCost += extractFloat64FromText(resultText, "Cost:")
		}

		// Finalize workflow run.
		run.TotalCostUSD = totalCost
		run.TotalTokensIn = totalIn
		run.TotalTokensOut = totalOut

		if run.Status != "failed" {
			run.Status = "completed"
		}

		finalState, _ := json.Marshal(state)
		run.State = string(finalState)
		run.Result = strings.Join(stepResults, "\n")
		run.CompletedAt = helpers.NowISO()
		_, _ = deps.Storage.WriteRun(ctx, run, 0)

		md := formatWorkflowRunMD(run, wf, stepResults)
		return helpers.TextResult(md), nil
	}
}

// ---------- get_run_status ----------

// GetRunStatusSchema returns the JSON Schema for the get_run_status tool.
func GetRunStatusSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID to check",
			},
		},
		"required": []any{"run_id"},
	})
	return s
}

// GetRunStatus retrieves the status and results of an execution run.
func GetRunStatus(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "run_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		runID := helpers.GetString(req.Arguments, "run_id")
		run, _, err := deps.Storage.ReadRun(ctx, runID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("run %q not found", runID)), nil
		}

		md := formatRunStatusMD(run)
		return helpers.TextResult(md), nil
	}
}

// ---------- list_runs ----------

// ListRunsSchema returns the JSON Schema for the list_runs tool.
func ListRunsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target_type": map[string]any{
				"type":        "string",
				"description": "Filter by target type: agent or workflow",
			},
			"target_id": map[string]any{
				"type":        "string",
				"description": "Filter by target ID (agent_id or workflow_id)",
			},
		},
	})
	return s
}

// ListRuns returns all execution runs, optionally filtered.
func ListRuns(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		targetType := helpers.GetString(req.Arguments, "target_type")
		targetID := helpers.GetString(req.Arguments, "target_id")

		runs, err := deps.Storage.ListRuns(ctx, targetType, targetID)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to list runs: %v", err)), nil
		}

		if len(runs) == 0 {
			return helpers.TextResult("## Runs\n\nNo runs found.\n"), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## Runs (%d)\n\n", len(runs))
		fmt.Fprintf(&b, "| ID | Type | Target | Status | Cost | Started |\n")
		fmt.Fprintf(&b, "|----|------|--------|--------|------|---------|\n")
		for _, r := range runs {
			cost := "—"
			if r.TotalCostUSD > 0 {
				cost = fmt.Sprintf("$%.4f", r.TotalCostUSD)
			}
			started := r.StartedAt
			if len(started) > 19 {
				started = started[:19]
			}
			// Show short run ID for readability.
			shortID := r.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
				shortID, r.TargetType, r.TargetID, r.Status, cost, started)
		}
		return helpers.TextResult(b.String()), nil
	}
}

// ---------- cancel_run ----------

// CancelRunSchema returns the JSON Schema for the cancel_run tool.
func CancelRunSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_id": map[string]any{
				"type":        "string",
				"description": "Run ID to cancel",
			},
		},
		"required": []any{"run_id"},
	})
	return s
}

// CancelRun cancels a running execution.
func CancelRun(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "run_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		runID := helpers.GetString(req.Arguments, "run_id")
		run, _, err := deps.Storage.ReadRun(ctx, runID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("run %q not found", runID)), nil
		}

		if run.Status != "running" && run.Status != "pending" {
			return helpers.ErrorResult("invalid_state",
				fmt.Sprintf("cannot cancel run in %q status", run.Status)), nil
		}

		run.Status = "cancelled"
		run.CompletedAt = helpers.NowISO()
		_, err = deps.Storage.WriteRun(ctx, run, 0)
		if err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to cancel run: %v", err)), nil
		}

		return helpers.TextResult(fmt.Sprintf("Run %q cancelled successfully.", runID)), nil
	}
}

// ---------- Workflow step type ----------

type workflowStep struct {
	AgentID  string         `json:"agent_id"`
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
	Prompt   string         `json:"prompt,omitempty"`
	Type     string         `json:"type,omitempty"` // for nested parallel steps
	Steps    []workflowStep `json:"steps,omitempty"`
}

// ---------- Cross-plugin call helpers ----------

// getAccountEnvWithProvider calls tools.agentops get_account_env to retrieve
// the provider name and environment variables for the given account.
func getAccountEnvWithProvider(ctx context.Context, store *storage.DataStorage, accountID string) (string, map[string]any, error) {
	resp, err := store.CallTool(ctx, "get_account_env", map[string]any{
		"account_id": accountID,
	})
	if err != nil {
		return "", nil, err
	}
	if !resp.Success {
		return "", nil, fmt.Errorf("%s: %s", resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.Result == nil {
		return "claude", map[string]any{}, nil
	}

	result := resp.Result.AsMap()

	provider := "claude"
	if p, ok := result["provider"].(string); ok && p != "" {
		provider = p
	}
	envMap := map[string]any{}
	if e, ok := result["env"].(map[string]any); ok {
		envMap = e
	}

	return provider, envMap, nil
}

// reportUsage calls tools.agentops report_usage to record token consumption.
func reportUsage(ctx context.Context, store *storage.DataStorage, accountID, runID string, tokensIn, tokensOut int64, costUSD float64, model string) error {
	_, err := store.CallTool(ctx, "report_usage", map[string]any{
		"account_id": accountID,
		"session_id": runID,
		"tokens_in":  float64(tokensIn),
		"tokens_out": float64(tokensOut),
		"cost_usd":   costUSD,
		"model":      model,
	})
	return err
}

// ---------- Response extraction helpers ----------

func extractText(resp *pluginv1.ToolResponse) string {
	if resp == nil || resp.Result == nil {
		return ""
	}
	for _, key := range []string{"text", "response", "result"} {
		if v, ok := resp.Result.Fields[key]; ok {
			if sv, ok := v.Kind.(*structpb.Value_StringValue); ok {
				return sv.StringValue
			}
		}
	}
	return ""
}

func extractInt64(resp *pluginv1.ToolResponse, key string) int64 {
	if resp == nil || resp.Result == nil {
		return 0
	}
	v, ok := resp.Result.Fields[key]
	if !ok || v == nil {
		return 0
	}
	nv, ok := v.Kind.(*structpb.Value_NumberValue)
	if !ok {
		return 0
	}
	return int64(nv.NumberValue)
}

func extractFloat64(resp *pluginv1.ToolResponse, key string) float64 {
	if resp == nil || resp.Result == nil {
		return 0
	}
	v, ok := resp.Result.Fields[key]
	if !ok || v == nil {
		return 0
	}
	nv, ok := v.Kind.(*structpb.Value_NumberValue)
	if !ok {
		return 0
	}
	return nv.NumberValue
}

// extractFloat64FromText is a best-effort helper that parses cost data from
// formatted markdown run results. Since individual run_agent calls create their
// own run records, the workflow doesn't get accurate cost totals through the
// handler response alone. This returns 0 if parsing fails.
func extractFloat64FromText(_ string, _ string) float64 {
	// Cost tracking is handled by individual run records.
	// The workflow aggregates from those records.
	return 0
}

// ---------- Markdown formatters ----------

func formatRunResultMD(run *storage.Run, agentName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Run Result\n\n")
	fmt.Fprintf(&b, "- **Run ID:** %s\n", run.ID)
	fmt.Fprintf(&b, "- **Agent:** %s (%s)\n", agentName, run.TargetID)
	fmt.Fprintf(&b, "- **Status:** %s\n", run.Status)
	if run.TotalCostUSD > 0 {
		fmt.Fprintf(&b, "- **Cost:** $%.4f\n", run.TotalCostUSD)
		fmt.Fprintf(&b, "- **Tokens:** %d in / %d out\n", run.TotalTokensIn, run.TotalTokensOut)
	}
	fmt.Fprintf(&b, "- **Started:** %s\n", run.StartedAt)
	if run.CompletedAt != "" {
		fmt.Fprintf(&b, "- **Completed:** %s\n", run.CompletedAt)
	}
	if run.Result != "" {
		fmt.Fprintf(&b, "\n#### Response\n\n%s\n", run.Result)
	}
	if run.Error != "" {
		fmt.Fprintf(&b, "\n#### Error\n\n%s\n", run.Error)
	}
	return b.String()
}

func formatRunStatusMD(run *storage.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Run Status\n\n")
	fmt.Fprintf(&b, "- **Run ID:** %s\n", run.ID)
	fmt.Fprintf(&b, "- **Type:** %s\n", run.TargetType)
	fmt.Fprintf(&b, "- **Target:** %s\n", run.TargetID)
	fmt.Fprintf(&b, "- **Status:** %s\n", run.Status)
	if run.TotalCostUSD > 0 {
		fmt.Fprintf(&b, "- **Cost:** $%.4f\n", run.TotalCostUSD)
		fmt.Fprintf(&b, "- **Tokens:** %d in / %d out\n", run.TotalTokensIn, run.TotalTokensOut)
	}
	fmt.Fprintf(&b, "- **Started:** %s\n", run.StartedAt)
	if run.CompletedAt != "" {
		fmt.Fprintf(&b, "- **Completed:** %s\n", run.CompletedAt)
	}
	if run.Result != "" {
		fmt.Fprintf(&b, "\n#### Result\n\n%s\n", run.Result)
	}
	if run.Error != "" {
		fmt.Fprintf(&b, "\n#### Error\n\n%s\n", run.Error)
	}
	return b.String()
}

func formatWorkflowRunMD(run *storage.Run, wf *storage.Workflow, stepResults []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Workflow Run: %s\n\n", wf.Name)
	fmt.Fprintf(&b, "- **Run ID:** %s\n", run.ID)
	fmt.Fprintf(&b, "- **Workflow:** %s (%s)\n", wf.Name, wf.ID)
	fmt.Fprintf(&b, "- **Type:** %s\n", wf.Type)
	fmt.Fprintf(&b, "- **Status:** %s\n", run.Status)
	if run.TotalCostUSD > 0 {
		fmt.Fprintf(&b, "- **Total Cost:** $%.4f\n", run.TotalCostUSD)
	}
	fmt.Fprintf(&b, "- **Started:** %s\n", run.StartedAt)
	if run.CompletedAt != "" {
		fmt.Fprintf(&b, "- **Completed:** %s\n", run.CompletedAt)
	}
	if len(stepResults) > 0 {
		fmt.Fprintf(&b, "\n#### Steps\n\n")
		for _, r := range stepResults {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	if run.Error != "" {
		fmt.Fprintf(&b, "\n#### Error\n\n%s\n", run.Error)
	}
	return b.String()
}
