// Package storage provides an abstraction over the orchestrator's storage and
// tool-call protocols for reading and writing agent, workflow, and run data.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"google.golang.org/protobuf/types/known/structpb"
)

// StorageClient sends requests to the orchestrator for storage and tool-call
// operations.
type StorageClient interface {
	Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error)
}

// DataStorage wraps the storage client for agent orchestrator operations.
type DataStorage struct {
	client StorageClient
}

// NewDataStorage creates a new DataStorage with the given client.
func NewDataStorage(client StorageClient) *DataStorage {
	return &DataStorage{client: client}
}

// ---------- Agent type ----------

// Agent represents an AI agent definition.
type Agent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	AccountID   string   `json:"account_id"`
	Tools       []string `json:"tools"`
	MaxBudget   float64  `json:"max_budget"`
	OutputKey   string   `json:"output_key"`
	Instruction string   `json:"instruction"` // stored as body, not metadata
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// ---------- Workflow type ----------

// Workflow represents a multi-agent workflow definition.
type Workflow struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"` // sequential, parallel, loop
	Steps       string  `json:"steps"` // JSON-serialized step array
	MaxBudget   float64 `json:"max_budget"`
	Description string  `json:"description"` // stored as body
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ---------- Run type ----------

// Run represents an execution run record.
type Run struct {
	ID            string  `json:"id"`
	TargetType    string  `json:"target_type"` // agent or workflow
	TargetID      string  `json:"target_id"`
	Status        string  `json:"status"` // pending, running, completed, failed, cancelled
	State         string  `json:"state"`  // JSON-serialized state map
	Result        string  `json:"result"`
	Error         string  `json:"error"`
	StartedAt     string  `json:"started_at"`
	CompletedAt   string  `json:"completed_at"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTokensIn int64   `json:"total_tokens_in"`
	TotalTokensOut int64  `json:"total_tokens_out"`
}

// ---------- Test Suite types ----------

// TestCase defines a single test case input + expected output assertions.
type TestCase struct {
	Name        string   `json:"name"`
	Prompt      string   `json:"prompt"`
	State       string   `json:"state"`        // JSON-encoded input state
	Contains    []string `json:"contains"`     // response must include all these strings
	NotContains []string `json:"not_contains"` // response must not include any of these
	Regex       []string `json:"regex"`        // response must match all these regex patterns
	MinLength   int      `json:"min_length"`   // response must be at least this many chars
}

// TestSuite defines a collection of test cases for an agent or workflow.
type TestSuite struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TargetType string     `json:"target_type"` // agent or workflow
	TargetID   string     `json:"target_id"`
	TestCases  []TestCase `json:"test_cases"` // stored as JSON in body
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}

// CaseResult is the result of running a single test case.
type CaseResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Reasons  []string `json:"reasons"`  // failure reasons (empty if passed)
	Response string   `json:"response"` // actual LLM response (truncated)
}

// TestResult holds the overall results of running a test suite.
type TestResult struct {
	ID        string       `json:"id"`
	SuiteID   string       `json:"suite_id"`
	SuiteName string       `json:"suite_name"`
	Passed    int          `json:"passed"`
	Failed    int          `json:"failed"`
	Total     int          `json:"total"`
	Cases     []CaseResult `json:"cases"`
	CreatedAt string       `json:"created_at"`
	DryRun    bool         `json:"dry_run"`
}

// ---------- Agent operations ----------

// ReadAgent loads an agent by ID from storage.
func (ds *DataStorage) ReadAgent(ctx context.Context, agentID string) (*Agent, int64, error) {
	path := agentPath(agentID)
	resp, err := ds.storageRead(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("read agent %s: %w", agentID, err)
	}

	agent, err := metadataToAgent(resp.Metadata)
	if err != nil {
		return nil, 0, fmt.Errorf("parse agent %s: %w", agentID, err)
	}
	// Body contains the instruction text.
	if len(resp.Content) > 0 {
		agent.Instruction = strings.TrimSpace(string(resp.Content))
	}
	return agent, resp.Version, nil
}

// WriteAgent persists an agent to storage.
func (ds *DataStorage) WriteAgent(ctx context.Context, agent *Agent, expectedVersion int64) (int64, error) {
	meta, err := agentToMetadata(agent)
	if err != nil {
		return 0, fmt.Errorf("encode agent: %w", err)
	}
	path := agentPath(agent.ID)
	body := agent.Instruction
	return ds.storageWrite(ctx, path, meta, []byte(body), expectedVersion)
}

// ListAgents returns all agents from storage.
func (ds *DataStorage) ListAgents(ctx context.Context) ([]*Agent, error) {
	prefix := "agents/agents/"
	entries, err := ds.storageList(ctx, prefix, "*.md")
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	var agents []*Agent
	for _, entry := range entries {
		base := filepath.Base(entry.Path)
		if !strings.HasSuffix(base, ".md") {
			continue
		}
		agentID := strings.TrimSuffix(base, ".md")

		agent, _, err := ds.ReadAgent(ctx, agentID)
		if err != nil {
			continue
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// DeleteAgent removes an agent from storage.
func (ds *DataStorage) DeleteAgent(ctx context.Context, agentID string) error {
	path := agentPath(agentID)
	return ds.storageDelete(ctx, path)
}

// ---------- Workflow operations ----------

// ReadWorkflow loads a workflow by ID from storage.
func (ds *DataStorage) ReadWorkflow(ctx context.Context, workflowID string) (*Workflow, int64, error) {
	path := workflowPath(workflowID)
	resp, err := ds.storageRead(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("read workflow %s: %w", workflowID, err)
	}

	wf, err := metadataToWorkflow(resp.Metadata)
	if err != nil {
		return nil, 0, fmt.Errorf("parse workflow %s: %w", workflowID, err)
	}
	if len(resp.Content) > 0 {
		wf.Description = strings.TrimSpace(string(resp.Content))
	}
	return wf, resp.Version, nil
}

// WriteWorkflow persists a workflow to storage.
func (ds *DataStorage) WriteWorkflow(ctx context.Context, wf *Workflow, expectedVersion int64) (int64, error) {
	meta, err := workflowToMetadata(wf)
	if err != nil {
		return 0, fmt.Errorf("encode workflow: %w", err)
	}
	path := workflowPath(wf.ID)
	body := wf.Description
	return ds.storageWrite(ctx, path, meta, []byte(body), expectedVersion)
}

// ListWorkflows returns all workflows from storage.
func (ds *DataStorage) ListWorkflows(ctx context.Context) ([]*Workflow, error) {
	prefix := "agents/workflows/"
	entries, err := ds.storageList(ctx, prefix, "*.md")
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}

	var workflows []*Workflow
	for _, entry := range entries {
		base := filepath.Base(entry.Path)
		if !strings.HasSuffix(base, ".md") {
			continue
		}
		wfID := strings.TrimSuffix(base, ".md")

		wf, _, err := ds.ReadWorkflow(ctx, wfID)
		if err != nil {
			continue
		}
		workflows = append(workflows, wf)
	}
	return workflows, nil
}

// DeleteWorkflow removes a workflow from storage.
func (ds *DataStorage) DeleteWorkflow(ctx context.Context, workflowID string) error {
	path := workflowPath(workflowID)
	return ds.storageDelete(ctx, path)
}

// ---------- Run operations ----------

// ReadRun loads a run by ID from storage.
func (ds *DataStorage) ReadRun(ctx context.Context, runID string) (*Run, int64, error) {
	path := runPath(runID)
	resp, err := ds.storageRead(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("read run %s: %w", runID, err)
	}

	run, err := metadataToRun(resp.Metadata)
	if err != nil {
		return nil, 0, fmt.Errorf("parse run %s: %w", runID, err)
	}
	return run, resp.Version, nil
}

// WriteRun persists a run to storage. Uses upsert (-1) so status updates
// on an existing run always succeed without a version read-modify-write cycle.
func (ds *DataStorage) WriteRun(ctx context.Context, run *Run, _ int64) (int64, error) {
	meta, err := runToMetadata(run)
	if err != nil {
		return 0, fmt.Errorf("encode run: %w", err)
	}
	path := runPath(run.ID)
	body := fmt.Sprintf("# Run: %s\n\nTarget: %s/%s | Status: %s\n",
		run.ID, run.TargetType, run.TargetID, run.Status)
	if run.Result != "" {
		body += fmt.Sprintf("\n## Result\n\n%s\n", run.Result)
	}
	if run.Error != "" {
		body += fmt.Sprintf("\n## Error\n\n%s\n", run.Error)
	}
	return ds.storageWrite(ctx, path, meta, []byte(body), -1)
}

// ListRuns returns all runs from storage, optionally filtered by target type and/or target ID.
func (ds *DataStorage) ListRuns(ctx context.Context, targetType, targetID string) ([]*Run, error) {
	prefix := "agents/runs/"
	entries, err := ds.storageList(ctx, prefix, "*.md")
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	var runs []*Run
	for _, entry := range entries {
		base := filepath.Base(entry.Path)
		if !strings.HasSuffix(base, ".md") {
			continue
		}
		runID := strings.TrimSuffix(base, ".md")

		run, _, err := ds.ReadRun(ctx, runID)
		if err != nil {
			continue
		}

		if targetType != "" && run.TargetType != targetType {
			continue
		}
		if targetID != "" && run.TargetID != targetID {
			continue
		}

		runs = append(runs, run)
	}
	return runs, nil
}

// ---------- TestSuite operations ----------

// ReadTestSuite loads a test suite by ID from storage.
func (ds *DataStorage) ReadTestSuite(ctx context.Context, suiteID string) (*TestSuite, int64, error) {
	path := testSuitePath(suiteID)
	resp, err := ds.storageRead(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("read test suite %s: %w", suiteID, err)
	}
	suite, err := metadataToTestSuite(resp.Metadata)
	if err != nil {
		return nil, 0, fmt.Errorf("parse test suite %s: %w", suiteID, err)
	}
	// Test cases are stored as JSON in the body.
	if len(resp.Content) > 0 {
		_ = json.Unmarshal(resp.Content, &suite.TestCases)
	}
	return suite, resp.Version, nil
}

// WriteTestSuite persists a test suite to storage.
func (ds *DataStorage) WriteTestSuite(ctx context.Context, suite *TestSuite, expectedVersion int64) (int64, error) {
	meta, err := testSuiteToMetadata(suite)
	if err != nil {
		return 0, fmt.Errorf("encode test suite: %w", err)
	}
	path := testSuitePath(suite.ID)
	body, _ := json.Marshal(suite.TestCases)
	return ds.storageWrite(ctx, path, meta, body, expectedVersion)
}

// ListTestSuites returns all test suites from storage.
func (ds *DataStorage) ListTestSuites(ctx context.Context) ([]*TestSuite, error) {
	prefix := "agents/test-suites/"
	entries, err := ds.storageList(ctx, prefix, "*.md")
	if err != nil {
		return nil, fmt.Errorf("list test suites: %w", err)
	}
	var suites []*TestSuite
	for _, entry := range entries {
		base := filepath.Base(entry.Path)
		if !strings.HasSuffix(base, ".md") {
			continue
		}
		suiteID := strings.TrimSuffix(base, ".md")
		suite, _, err := ds.ReadTestSuite(ctx, suiteID)
		if err != nil {
			continue
		}
		suites = append(suites, suite)
	}
	return suites, nil
}

// WriteTestResult persists a test result to storage. Uses upsert (-1).
func (ds *DataStorage) WriteTestResult(ctx context.Context, result *TestResult) (int64, error) {
	meta, err := testResultToMetadata(result)
	if err != nil {
		return 0, fmt.Errorf("encode test result: %w", err)
	}
	path := testResultPath(result.ID)
	body, _ := json.Marshal(result.Cases)
	return ds.storageWrite(ctx, path, meta, body, -1)
}

// ReadTestResult loads a test result by ID from storage.
func (ds *DataStorage) ReadTestResult(ctx context.Context, resultID string) (*TestResult, error) {
	path := testResultPath(resultID)
	resp, err := ds.storageRead(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read test result %s: %w", resultID, err)
	}
	result, err := metadataToTestResult(resp.Metadata)
	if err != nil {
		return nil, fmt.Errorf("parse test result %s: %w", resultID, err)
	}
	if len(resp.Content) > 0 {
		_ = json.Unmarshal(resp.Content, &result.Cases)
	}
	return result, nil
}

// ---------- Cross-plugin tool calls ----------

// CallTool sends a ToolRequest to the orchestrator, which routes it to the
// appropriate plugin.
func (ds *DataStorage) CallTool(ctx context.Context, toolName string, args map[string]any) (*pluginv1.ToolResponse, error) {
	return ds.CallToolWithProvider(ctx, toolName, args, "")
}

// CallToolWithProvider sends a ToolRequest with a specific provider, enabling
// the orchestrator to route AI tool calls to the correct bridge plugin.
func (ds *DataStorage) CallToolWithProvider(ctx context.Context, toolName string, args map[string]any, provider string) (*pluginv1.ToolResponse, error) {
	argsStruct, err := structpb.NewStruct(args)
	if err != nil {
		return nil, fmt.Errorf("build args struct for %s: %w", toolName, err)
	}

	req := &pluginv1.PluginRequest{
		RequestId: helpers.NewUUID(),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: &pluginv1.ToolRequest{
				ToolName:  toolName,
				Arguments: argsStruct,
				Provider:  provider,
			},
		},
	}
	resp, err := ds.client.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", toolName, err)
	}
	tc := resp.GetToolCall()
	if tc == nil {
		return nil, fmt.Errorf("unexpected response type for tool call %s", toolName)
	}
	return tc, nil
}

// ---------- Low-level storage protocol ----------

func (ds *DataStorage) storageRead(ctx context.Context, path string) (*pluginv1.StorageReadResponse, error) {
	resp, err := ds.client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: helpers.NewUUID(),
		Request: &pluginv1.PluginRequest_StorageRead{
			StorageRead: &pluginv1.StorageReadRequest{
				Path:        path,
				StorageType: "markdown",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	sr := resp.GetStorageRead()
	if sr == nil {
		return nil, fmt.Errorf("unexpected response type for storage read")
	}
	return sr, nil
}

func (ds *DataStorage) storageWrite(ctx context.Context, path string, metadata *structpb.Struct, content []byte, expectedVersion int64) (int64, error) {
	resp, err := ds.client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: helpers.NewUUID(),
		Request: &pluginv1.PluginRequest_StorageWrite{
			StorageWrite: &pluginv1.StorageWriteRequest{
				Path:            path,
				Content:         content,
				Metadata:        metadata,
				ExpectedVersion: expectedVersion,
				StorageType:     "markdown",
			},
		},
	})
	if err != nil {
		return 0, err
	}
	sw := resp.GetStorageWrite()
	if sw == nil {
		return 0, fmt.Errorf("unexpected response type for storage write")
	}
	if !sw.Success {
		return 0, fmt.Errorf("storage write failed: %s", sw.Error)
	}
	return sw.NewVersion, nil
}

func (ds *DataStorage) storageDelete(ctx context.Context, path string) error {
	resp, err := ds.client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: helpers.NewUUID(),
		Request: &pluginv1.PluginRequest_StorageDelete{
			StorageDelete: &pluginv1.StorageDeleteRequest{
				Path:        path,
				StorageType: "markdown",
			},
		},
	})
	if err != nil {
		return err
	}
	sd := resp.GetStorageDelete()
	if sd == nil {
		return fmt.Errorf("unexpected response type for storage delete")
	}
	if !sd.Success {
		return fmt.Errorf("storage delete failed")
	}
	return nil
}

func (ds *DataStorage) storageList(ctx context.Context, prefix, pattern string) ([]*pluginv1.StorageEntry, error) {
	resp, err := ds.client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: helpers.NewUUID(),
		Request: &pluginv1.PluginRequest_StorageList{
			StorageList: &pluginv1.StorageListRequest{
				Prefix:      prefix,
				Pattern:     pattern,
				StorageType: "markdown",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	sl := resp.GetStorageList()
	if sl == nil {
		return nil, fmt.Errorf("unexpected response type for storage list")
	}
	return sl.Entries, nil
}

// ---------- Path helpers ----------

func agentPath(agentID string) string {
	return fmt.Sprintf("agents/agents/%s.md", agentID)
}

func workflowPath(workflowID string) string {
	return fmt.Sprintf("agents/workflows/%s.md", workflowID)
}

func runPath(runID string) string {
	return fmt.Sprintf("agents/runs/%s.md", runID)
}

func testSuitePath(suiteID string) string {
	return fmt.Sprintf("agents/test-suites/%s.md", suiteID)
}

func testResultPath(resultID string) string {
	return fmt.Sprintf("agents/test-results/%s.md", resultID)
}

// ---------- Metadata conversion helpers ----------

func agentToMetadata(a *Agent) (*structpb.Struct, error) {
	m := map[string]any{
		"id":         a.ID,
		"name":       a.Name,
		"provider":   a.Provider,
		"model":      a.Model,
		"account_id": a.AccountID,
		"max_budget": a.MaxBudget,
		"output_key": a.OutputKey,
		"created_at": a.CreatedAt,
		"updated_at": a.UpdatedAt,
	}
	if len(a.Tools) > 0 {
		tools := make([]any, len(a.Tools))
		for i, t := range a.Tools {
			tools[i] = t
		}
		m["tools"] = tools
	}
	return structpb.NewStruct(m)
}

func metadataToAgent(meta *structpb.Struct) (*Agent, error) {
	if meta == nil {
		return nil, fmt.Errorf("no metadata")
	}
	raw, err := json.Marshal(meta.AsMap())
	if err != nil {
		return nil, err
	}
	var a Agent
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func workflowToMetadata(wf *Workflow) (*structpb.Struct, error) {
	m := map[string]any{
		"id":         wf.ID,
		"name":       wf.Name,
		"type":       wf.Type,
		"steps":      wf.Steps,
		"max_budget": wf.MaxBudget,
		"created_at": wf.CreatedAt,
		"updated_at": wf.UpdatedAt,
	}
	return structpb.NewStruct(m)
}

func metadataToWorkflow(meta *structpb.Struct) (*Workflow, error) {
	if meta == nil {
		return nil, fmt.Errorf("no metadata")
	}
	raw, err := json.Marshal(meta.AsMap())
	if err != nil {
		return nil, err
	}
	var wf Workflow
	if err := json.Unmarshal(raw, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

func runToMetadata(r *Run) (*structpb.Struct, error) {
	m := map[string]any{
		"id":               r.ID,
		"target_type":      r.TargetType,
		"target_id":        r.TargetID,
		"status":           r.Status,
		"state":            r.State,
		"result":           r.Result,
		"error":            r.Error,
		"started_at":       r.StartedAt,
		"completed_at":     r.CompletedAt,
		"total_cost_usd":   r.TotalCostUSD,
		"total_tokens_in":  float64(r.TotalTokensIn),
		"total_tokens_out": float64(r.TotalTokensOut),
	}
	return structpb.NewStruct(m)
}

func metadataToRun(meta *structpb.Struct) (*Run, error) {
	if meta == nil {
		return nil, fmt.Errorf("no metadata")
	}
	raw, err := json.Marshal(meta.AsMap())
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func testSuiteToMetadata(s *TestSuite) (*structpb.Struct, error) {
	m := map[string]any{
		"id":          s.ID,
		"name":        s.Name,
		"target_type": s.TargetType,
		"target_id":   s.TargetID,
		"created_at":  s.CreatedAt,
		"updated_at":  s.UpdatedAt,
	}
	return structpb.NewStruct(m)
}

func metadataToTestSuite(meta *structpb.Struct) (*TestSuite, error) {
	if meta == nil {
		return nil, fmt.Errorf("no metadata")
	}
	raw, err := json.Marshal(meta.AsMap())
	if err != nil {
		return nil, err
	}
	var s TestSuite
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func testResultToMetadata(r *TestResult) (*structpb.Struct, error) {
	m := map[string]any{
		"id":         r.ID,
		"suite_id":   r.SuiteID,
		"suite_name": r.SuiteName,
		"passed":     float64(r.Passed),
		"failed":     float64(r.Failed),
		"total":      float64(r.Total),
		"created_at": r.CreatedAt,
		"dry_run":    r.DryRun,
	}
	return structpb.NewStruct(m)
}

func metadataToTestResult(meta *structpb.Struct) (*TestResult, error) {
	if meta == nil {
		return nil, fmt.Errorf("no metadata")
	}
	raw, err := json.Marshal(meta.AsMap())
	if err != nil {
		return nil, err
	}
	var r TestResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
