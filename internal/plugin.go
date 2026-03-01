// Package internal contains the core registration logic for the agent.orchestrator
// plugin. The ToolsPlugin struct wires all 14 agent orchestration tool handlers
// to the plugin builder with their schemas and descriptions.
package internal

import (
	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/tools"
)

// ToolsPlugin holds the shared dependencies for all tool handlers.
type ToolsPlugin struct {
	Storage *storage.DataStorage
}

// RegisterTools registers all 20 agent orchestration tools on the given plugin builder.
func (tp *ToolsPlugin) RegisterTools(builder *plugin.PluginBuilder) {
	deps := &tools.ToolDeps{Storage: tp.Storage}

	// --- Agent CRUD (4) ---
	builder.RegisterTool("define_agent",
		"Define or update an AI agent with provider, model, and instruction",
		tools.DefineAgentSchema(), tools.DefineAgent(deps))

	builder.RegisterTool("get_agent",
		"Get an agent definition by ID",
		tools.GetAgentSchema(), tools.GetAgent(deps))

	builder.RegisterTool("list_agents",
		"List all defined agents",
		tools.ListAgentsSchema(), tools.ListAgents(deps))

	builder.RegisterTool("delete_agent",
		"Delete an agent definition",
		tools.DeleteAgentSchema(), tools.DeleteAgent(deps))

	// --- Workflow CRUD (4) ---
	builder.RegisterTool("define_workflow",
		"Define or update a multi-agent workflow",
		tools.DefineWorkflowSchema(), tools.DefineWorkflow(deps))

	builder.RegisterTool("get_workflow",
		"Get a workflow definition by ID",
		tools.GetWorkflowSchema(), tools.GetWorkflow(deps))

	builder.RegisterTool("list_workflows",
		"List all defined workflows",
		tools.ListWorkflowsSchema(), tools.ListWorkflows(deps))

	builder.RegisterTool("delete_workflow",
		"Delete a workflow definition",
		tools.DeleteWorkflowSchema(), tools.DeleteWorkflow(deps))

	// --- Execution (5) ---
	builder.RegisterTool("run_agent",
		"Execute an agent with a prompt",
		tools.RunAgentSchema(), tools.RunAgent(deps))

	builder.RegisterTool("run_workflow",
		"Execute a multi-agent workflow",
		tools.RunWorkflowSchema(), tools.RunWorkflow(deps))

	builder.RegisterTool("get_run_status",
		"Get the status and results of an execution run",
		tools.GetRunStatusSchema(), tools.GetRunStatus(deps))

	builder.RegisterTool("list_runs",
		"List execution runs",
		tools.ListRunsSchema(), tools.ListRuns(deps))

	builder.RegisterTool("cancel_run",
		"Cancel a running execution",
		tools.CancelRunSchema(), tools.CancelRun(deps))

	// --- Discovery (1) ---
	builder.RegisterTool("list_available_models",
		"List known models for each AI provider",
		tools.ListAvailableModelsSchema(), tools.ListAvailableModels(deps))

	// --- Testing (6) ---
	builder.RegisterTool("create_test_suite",
		"Create a test suite for an agent or workflow",
		tools.CreateTestSuiteSchema(), tools.CreateTestSuite(deps))

	builder.RegisterTool("run_test_suite",
		"Run all test cases in a test suite",
		tools.RunTestSuiteSchema(), tools.RunTestSuite(deps))

	builder.RegisterTool("get_test_results",
		"Get test results by result ID",
		tools.GetTestResultsSchema(), tools.GetTestResults(deps))

	builder.RegisterTool("add_test_case",
		"Add a test case to an existing test suite",
		tools.AddTestCaseSchema(), tools.AddTestCase(deps))

	builder.RegisterTool("evaluate_response",
		"Evaluate a response against assertions without running a full test suite",
		tools.EvaluateResponseSchema(), tools.EvaluateResponse(deps))

	builder.RegisterTool("compare_providers",
		"Run the same prompt across multiple AI providers and compare results",
		tools.CompareProvidersSchema(), tools.CompareProviders(deps))
}
