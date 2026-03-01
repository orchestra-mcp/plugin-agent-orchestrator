package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/plugin-agent-orchestrator/internal/storage"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"google.golang.org/protobuf/types/known/structpb"
)

// ---------- create_test_suite ----------

// CreateTestSuiteSchema returns the JSON Schema for the create_test_suite tool.
func CreateTestSuiteSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Display name for the test suite",
			},
			"target_type": map[string]any{
				"type":        "string",
				"enum":        []any{"agent", "workflow"},
				"description": "Whether this suite tests an agent or workflow",
			},
			"target_id": map[string]any{
				"type":        "string",
				"description": "ID of the agent or workflow to test",
			},
			"test_cases": map[string]any{
				"type":        "array",
				"description": "Optional initial test cases (array of {name, prompt, state, contains, not_contains, regex, min_length})",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":         map[string]any{"type": "string"},
						"prompt":       map[string]any{"type": "string"},
						"state":        map[string]any{"type": "string"},
						"contains":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"not_contains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"regex":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"min_length":   map[string]any{"type": "number"},
					},
				},
			},
		},
		"required": []any{"name", "target_type", "target_id"},
	})
	return s
}

// CreateTestSuite creates a new test suite for an agent or workflow.
func CreateTestSuite(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "name", "target_type", "target_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		name := helpers.GetString(req.Arguments, "name")
		targetType := helpers.GetString(req.Arguments, "target_type")
		targetID := helpers.GetString(req.Arguments, "target_id")

		if targetType != "agent" && targetType != "workflow" {
			return helpers.ErrorResult("validation_error", "target_type must be \"agent\" or \"workflow\""), nil
		}

		// Parse test_cases from the arguments. structpb list values are best
		// accessed via AsMap() which converts to plain Go types, then JSON
		// round-trip into the storage type.
		var testCases []storage.TestCase
		if req.Arguments != nil {
			argsMap := req.Arguments.AsMap()
			if raw, err := json.Marshal(argsMap["test_cases"]); err == nil && string(raw) != "null" {
				_ = json.Unmarshal(raw, &testCases)
			}
		}
		if testCases == nil {
			testCases = []storage.TestCase{}
		}

		now := helpers.NowISO()
		// Generate suite ID: STE- + 4 random uppercase hex chars derived from UUID.
		suiteID := "STE-" + strings.ToUpper(helpers.NewUUID()[:4])

		suite := &storage.TestSuite{
			ID:         suiteID,
			Name:       name,
			TargetType: targetType,
			TargetID:   targetID,
			TestCases:  testCases,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if _, err := deps.Storage.WriteTestSuite(ctx, suite, 0); err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to save test suite: %v", err)), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "### Test Suite Created\n\n")
		fmt.Fprintf(&b, "- **ID:** %s\n", suite.ID)
		fmt.Fprintf(&b, "- **Name:** %s\n", suite.Name)
		fmt.Fprintf(&b, "- **Target:** %s/%s\n", suite.TargetType, suite.TargetID)
		fmt.Fprintf(&b, "- **Test Cases:** %d\n", len(suite.TestCases))
		fmt.Fprintf(&b, "- **Created:** %s\n", suite.CreatedAt)
		return helpers.TextResult(b.String()), nil
	}
}

// ---------- run_test_suite ----------

// RunTestSuiteSchema returns the JSON Schema for the run_test_suite tool.
func RunTestSuiteSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"suite_id": map[string]any{
				"type":        "string",
				"description": "ID of the test suite to run",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "If true, return mock responses without calling any LLM",
			},
		},
		"required": []any{"suite_id"},
	})
	return s
}

// RunTestSuite executes all test cases in a suite and records results.
func RunTestSuite(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "suite_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		suiteID := helpers.GetString(req.Arguments, "suite_id")
		dryRun := helpers.GetBool(req.Arguments, "dry_run")

		// Load suite from storage.
		suite, _, err := deps.Storage.ReadTestSuite(ctx, suiteID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("test suite %q not found", suiteID)), nil
		}

		// Verify target exists.
		switch suite.TargetType {
		case "agent":
			if _, _, err := deps.Storage.ReadAgent(ctx, suite.TargetID); err != nil {
				return helpers.ErrorResult("not_found",
					fmt.Sprintf("target agent %q not found", suite.TargetID)), nil
			}
		case "workflow":
			if _, _, err := deps.Storage.ReadWorkflow(ctx, suite.TargetID); err != nil {
				return helpers.ErrorResult("not_found",
					fmt.Sprintf("target workflow %q not found", suite.TargetID)), nil
			}
		}

		var caseResults []storage.CaseResult
		passed, failed := 0, 0

		for _, tc := range suite.TestCases {
			var response string

			if dryRun {
				// Build a mock response that satisfies contains assertions.
				response = fmt.Sprintf("[DRY RUN] Mock response for test case: %s. This response contains mock data.", tc.Name)
				if len(tc.Contains) > 0 {
					response += " " + strings.Join(tc.Contains, " ")
				}
			} else {
				// Call run_agent or run_workflow via the tool dispatch.
				var toolName string
				var toolArgs map[string]any
				switch suite.TargetType {
				case "agent":
					toolName = "run_agent"
					toolArgs = map[string]any{
						"agent_id": suite.TargetID,
						"prompt":   tc.Prompt,
					}
					if tc.State != "" {
						toolArgs["state"] = tc.State
					}
				case "workflow":
					toolName = "run_workflow"
					toolArgs = map[string]any{
						"workflow_id": suite.TargetID,
					}
					if tc.State != "" {
						toolArgs["state"] = tc.State
					}
				}

				toolResp, err := deps.Storage.CallTool(ctx, toolName, toolArgs)
				if err != nil {
					response = fmt.Sprintf("[ERROR] tool call failed: %v", err)
				} else {
					response = extractText(toolResp)
				}
			}

			failures := evaluateAssertions(response, tc)
			casePassed := len(failures) == 0

			// Truncate response for storage.
			truncated := response
			if len(truncated) > 500 {
				truncated = truncated[:500] + "..."
			}

			caseResults = append(caseResults, storage.CaseResult{
				Name:     tc.Name,
				Passed:   casePassed,
				Reasons:  failures,
				Response: truncated,
			})

			if casePassed {
				passed++
			} else {
				failed++
			}
		}

		result := &storage.TestResult{
			ID:        helpers.NewUUID(),
			SuiteID:   suiteID,
			SuiteName: suite.Name,
			Passed:    passed,
			Failed:    failed,
			Total:     len(suite.TestCases),
			Cases:     caseResults,
			CreatedAt: helpers.NowISO(),
			DryRun:    dryRun,
		}

		if _, err := deps.Storage.WriteTestResult(ctx, result); err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to save test result: %v", err)), nil
		}

		md := formatTestResultMD(result)
		return helpers.TextResult(md), nil
	}
}

// ---------- get_test_results ----------

// GetTestResultsSchema returns the JSON Schema for the get_test_results tool.
func GetTestResultsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result_id": map[string]any{
				"type":        "string",
				"description": "Test result ID to retrieve",
			},
		},
		"required": []any{"result_id"},
	})
	return s
}

// GetTestResults loads and formats a test result by ID.
func GetTestResults(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "result_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		resultID := helpers.GetString(req.Arguments, "result_id")
		result, err := deps.Storage.ReadTestResult(ctx, resultID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("test result %q not found", resultID)), nil
		}

		md := formatTestResultMD(result)
		return helpers.TextResult(md), nil
	}
}

// ---------- add_test_case ----------

// AddTestCaseSchema returns the JSON Schema for the add_test_case tool.
func AddTestCaseSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"suite_id": map[string]any{
				"type":        "string",
				"description": "ID of the test suite to add the case to",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Name for the test case",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Prompt to send to the agent or workflow",
			},
			"state": map[string]any{
				"type":        "string",
				"description": "JSON-encoded input state map",
			},
			"contains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of strings the response must contain",
			},
			"not_contains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of strings the response must not contain",
			},
			"regex": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of regex patterns the response must match",
			},
			"min_length": map[string]any{
				"type":        "number",
				"description": "Minimum character length the response must be",
			},
		},
		"required": []any{"suite_id", "name", "prompt"},
	})
	return s
}

// AddTestCase appends a new test case to an existing test suite.
func AddTestCase(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "suite_id", "name", "prompt"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		suiteID := helpers.GetString(req.Arguments, "suite_id")
		name := helpers.GetString(req.Arguments, "name")
		prompt := helpers.GetString(req.Arguments, "prompt")
		state := helpers.GetString(req.Arguments, "state")
		minLength := helpers.GetInt(req.Arguments, "min_length")

		argsMap := req.Arguments.AsMap()
		contains := parseStringSliceFromMap(argsMap, "contains")
		notContains := parseStringSliceFromMap(argsMap, "not_contains")
		regexPatterns := parseStringSliceFromMap(argsMap, "regex")

		// Load existing suite (also gets current version for CAS).
		suite, version, err := deps.Storage.ReadTestSuite(ctx, suiteID)
		if err != nil {
			return helpers.ErrorResult("not_found", fmt.Sprintf("test suite %q not found", suiteID)), nil
		}

		tc := storage.TestCase{
			Name:        name,
			Prompt:      prompt,
			State:       state,
			Contains:    contains,
			NotContains: notContains,
			Regex:       regexPatterns,
			MinLength:   minLength,
		}

		suite.TestCases = append(suite.TestCases, tc)
		suite.UpdatedAt = helpers.NowISO()

		if _, err := deps.Storage.WriteTestSuite(ctx, suite, version); err != nil {
			return helpers.ErrorResult("storage_error", fmt.Sprintf("failed to save test suite: %v", err)), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "### Test Case Added\n\n")
		fmt.Fprintf(&b, "- **Suite:** %s (%s)\n", suite.Name, suite.ID)
		fmt.Fprintf(&b, "- **Case Name:** %s\n", tc.Name)
		fmt.Fprintf(&b, "- **Prompt:** %s\n", tc.Prompt)
		if len(tc.Contains) > 0 {
			fmt.Fprintf(&b, "- **Must Contain:** %s\n", strings.Join(tc.Contains, ", "))
		}
		if len(tc.NotContains) > 0 {
			fmt.Fprintf(&b, "- **Must Not Contain:** %s\n", strings.Join(tc.NotContains, ", "))
		}
		if len(tc.Regex) > 0 {
			fmt.Fprintf(&b, "- **Regex Patterns:** %s\n", strings.Join(tc.Regex, ", "))
		}
		if tc.MinLength > 0 {
			fmt.Fprintf(&b, "- **Min Length:** %d chars\n", tc.MinLength)
		}
		fmt.Fprintf(&b, "- **Total Cases in Suite:** %d\n", len(suite.TestCases))
		return helpers.TextResult(b.String()), nil
	}
}

// ---------- evaluate_response ----------

// EvaluateResponseSchema returns the JSON Schema for the evaluate_response tool.
func EvaluateResponseSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"response": map[string]any{
				"type":        "string",
				"description": "The response text to evaluate",
			},
			"contains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of strings the response must contain",
			},
			"not_contains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of strings the response must not contain",
			},
			"regex": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of regex patterns the response must match",
			},
			"min_length": map[string]any{
				"type":        "number",
				"description": "Minimum character length the response must be",
			},
		},
		"required": []any{"response"},
	})
	return s
}

// EvaluateResponse runs assertions against a provided response text directly,
// without running a full test suite.
func EvaluateResponse(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		_ = deps // not needed for stateless assertion evaluation

		if err := helpers.ValidateRequired(req.Arguments, "response"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		response := helpers.GetString(req.Arguments, "response")
		minLength := helpers.GetInt(req.Arguments, "min_length")

		argsMap2 := req.Arguments.AsMap()
		contains := parseStringSliceFromMap(argsMap2, "contains")
		notContains := parseStringSliceFromMap(argsMap2, "not_contains")
		regexPatterns := parseStringSliceFromMap(argsMap2, "regex")

		tc := storage.TestCase{
			Name:        "ad-hoc",
			Prompt:      "",
			Contains:    contains,
			NotContains: notContains,
			Regex:       regexPatterns,
			MinLength:   minLength,
		}

		failures := evaluateAssertions(response, tc)

		var b strings.Builder
		fmt.Fprintf(&b, "## Response Evaluation\n\n")

		// Show assertion summary.
		total := len(contains) + len(notContains) + len(regexPatterns)
		if minLength > 0 {
			total++
		}

		if len(failures) == 0 {
			fmt.Fprintf(&b, "**Result: PASSED** — all %d assertions passed.\n\n", total)
		} else {
			fmt.Fprintf(&b, "**Result: FAILED** — %d of %d assertions failed.\n\n", len(failures), total)
		}

		if len(contains) > 0 {
			fmt.Fprintf(&b, "### Contains Assertions\n\n")
			lower := strings.ToLower(response)
			for _, s := range contains {
				if strings.Contains(lower, strings.ToLower(s)) {
					fmt.Fprintf(&b, "- PASS: response contains %q\n", s)
				} else {
					fmt.Fprintf(&b, "- FAIL: response missing %q\n", s)
				}
			}
			fmt.Fprintf(&b, "\n")
		}

		if len(notContains) > 0 {
			fmt.Fprintf(&b, "### Not-Contains Assertions\n\n")
			lower := strings.ToLower(response)
			for _, s := range notContains {
				if strings.Contains(lower, strings.ToLower(s)) {
					fmt.Fprintf(&b, "- FAIL: response contains forbidden %q\n", s)
				} else {
					fmt.Fprintf(&b, "- PASS: response does not contain %q\n", s)
				}
			}
			fmt.Fprintf(&b, "\n")
		}

		if len(regexPatterns) > 0 {
			fmt.Fprintf(&b, "### Regex Assertions\n\n")
			for _, pattern := range regexPatterns {
				re, err := regexp.Compile(pattern)
				if err != nil {
					fmt.Fprintf(&b, "- ERROR: invalid regex %q: %v\n", pattern, err)
				} else if re.MatchString(response) {
					fmt.Fprintf(&b, "- PASS: pattern %q matched\n", pattern)
				} else {
					fmt.Fprintf(&b, "- FAIL: pattern %q not matched\n", pattern)
				}
			}
			fmt.Fprintf(&b, "\n")
		}

		if minLength > 0 {
			fmt.Fprintf(&b, "### Length Assertion\n\n")
			if len(response) >= minLength {
				fmt.Fprintf(&b, "- PASS: response length %d >= %d\n\n", len(response), minLength)
			} else {
				fmt.Fprintf(&b, "- FAIL: response length %d < %d\n\n", len(response), minLength)
			}
		}

		if len(failures) > 0 {
			fmt.Fprintf(&b, "### Failures\n\n")
			for _, f := range failures {
				fmt.Fprintf(&b, "- %s\n", f)
			}
		}

		return helpers.TextResult(b.String()), nil
	}
}

// ---------- compare_providers ----------

// CompareProvidersSchema returns the JSON Schema for the compare_providers tool.
func CompareProvidersSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Prompt to send to each provider",
			},
			"providers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "JSON array of provider names (e.g. [\"claude\",\"openai\",\"gemini\"])",
			},
			"model_overrides": map[string]any{
				"type":        "string",
				"description": "JSON object mapping provider name to model override (e.g. {\"claude\":\"claude-haiku-4-5-20251001\"})",
			},
			"system_prompt": map[string]any{
				"type":        "string",
				"description": "Optional system prompt to use for all providers",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "If true, return mock responses without calling any LLM",
			},
		},
		"required": []any{"prompt", "providers"},
	})
	return s
}

// CompareProviders runs the same prompt across multiple AI providers and returns
// a side-by-side comparison.
func CompareProviders(deps *ToolDeps) ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "prompt", "providers"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}

		prompt := helpers.GetString(req.Arguments, "prompt")
		dryRun := helpers.GetBool(req.Arguments, "dry_run")
		systemPrompt := helpers.GetString(req.Arguments, "system_prompt")

		providers := parseStringSliceFromMap(req.Arguments.AsMap(), "providers")
		if len(providers) == 0 {
			return helpers.ErrorResult("validation_error", "providers must be a non-empty JSON array of provider names"), nil
		}

		// Parse model overrides (optional).
		modelOverrides := map[string]string{}
		if raw := helpers.GetString(req.Arguments, "model_overrides"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &modelOverrides)
		}

		type providerResult struct {
			Provider string
			Response string
			Error    string
		}

		results := make([]providerResult, 0, len(providers))

		for _, provider := range providers {
			if dryRun {
				results = append(results, providerResult{
					Provider: provider,
					Response: fmt.Sprintf("[DRY RUN] Mock response from %s: The answer is 42. This is a sample response for provider comparison.", provider),
				})
				continue
			}

			args := map[string]any{
				"prompt": prompt,
			}
			if model, ok := modelOverrides[provider]; ok && model != "" {
				args["model"] = model
			}
			if systemPrompt != "" {
				args["system_prompt"] = systemPrompt
			}

			toolResp, err := deps.Storage.CallToolWithProvider(ctx, "ai_prompt", args, provider)
			if err != nil {
				results = append(results, providerResult{
					Provider: provider,
					Error:    fmt.Sprintf("call failed: %v", err),
				})
				continue
			}
			if !toolResp.Success {
				results = append(results, providerResult{
					Provider: provider,
					Error:    fmt.Sprintf("%s: %s", toolResp.ErrorCode, toolResp.ErrorMessage),
				})
				continue
			}

			response := extractText(toolResp)
			results = append(results, providerResult{
				Provider: provider,
				Response: response,
			})
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## Provider Comparison\n\n")
		fmt.Fprintf(&b, "**Prompt:** %s\n\n", prompt)
		if dryRun {
			fmt.Fprintf(&b, "_Dry run mode — mock responses shown._\n\n")
		}

		fmt.Fprintf(&b, "| Provider | Length | Response (truncated) |\n")
		fmt.Fprintf(&b, "|----------|--------|----------------------|\n")
		for _, r := range results {
			if r.Error != "" {
				fmt.Fprintf(&b, "| %s | — | ERROR: %s |\n", r.Provider, r.Error)
				continue
			}
			truncated := r.Response
			if len(truncated) > 500 {
				truncated = truncated[:500] + "..."
			}
			// Escape pipe characters to avoid breaking the markdown table.
			truncated = strings.ReplaceAll(truncated, "|", "\\|")
			truncated = strings.ReplaceAll(truncated, "\n", " ")
			fmt.Fprintf(&b, "| %s | %d | %s |\n", r.Provider, len(r.Response), truncated)
		}

		fmt.Fprintf(&b, "\n")

		// Full responses section.
		fmt.Fprintf(&b, "## Full Responses\n\n")
		for _, r := range results {
			fmt.Fprintf(&b, "### %s\n\n", r.Provider)
			if r.Error != "" {
				fmt.Fprintf(&b, "**Error:** %s\n\n", r.Error)
			} else {
				full := r.Response
				if len(full) > 500 {
					full = full[:500] + "...\n_(truncated)_"
				}
				fmt.Fprintf(&b, "%s\n\n", full)
			}
		}

		return helpers.TextResult(b.String()), nil
	}
}

// ---------- evaluateAssertions ----------

// evaluateAssertions checks a response string against a TestCase's assertion
// fields and returns a list of failure reason strings. An empty slice means
// all assertions passed.
func evaluateAssertions(response string, tc storage.TestCase) []string {
	var failures []string
	lower := strings.ToLower(response)

	for _, s := range tc.Contains {
		if !strings.Contains(lower, strings.ToLower(s)) {
			failures = append(failures, fmt.Sprintf("missing required string: %q", s))
		}
	}

	for _, s := range tc.NotContains {
		if strings.Contains(lower, strings.ToLower(s)) {
			failures = append(failures, fmt.Sprintf("contains forbidden string: %q", s))
		}
	}

	for _, pattern := range tc.Regex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			failures = append(failures, fmt.Sprintf("invalid regex pattern %q: %v", pattern, err))
			continue
		}
		if !re.MatchString(response) {
			failures = append(failures, fmt.Sprintf("regex pattern not matched: %q", pattern))
		}
	}

	if tc.MinLength > 0 && len(response) < tc.MinLength {
		failures = append(failures, fmt.Sprintf("response too short: %d chars (min %d)", len(response), tc.MinLength))
	}

	return failures
}

// ---------- Helpers ----------

// parseStringSliceFromMap extracts a string slice from a map[string]any
// (produced by structpb AsMap()). The value may be a []any or a JSON string.
func parseStringSliceFromMap(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	// Native array from structpb.AsMap()
	if list, ok := v.([]any); ok {
		var result []string
		for _, item := range list {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	// Fallback: JSON string encoding
	if s, ok := v.(string); ok && s != "" {
		var result []string
		if err := json.Unmarshal([]byte(s), &result); err == nil {
			return result
		}
	}
	return nil
}

// formatTestResultMD formats a TestResult as a markdown report.
func formatTestResultMD(r *storage.TestResult) string {
	var b strings.Builder

	status := "PASSED"
	if r.Failed > 0 {
		status = "FAILED"
	}

	fmt.Fprintf(&b, "## Test Results: %s\n\n", r.SuiteName)
	fmt.Fprintf(&b, "- **Result ID:** %s\n", r.ID)
	fmt.Fprintf(&b, "- **Suite:** %s\n", r.SuiteID)
	fmt.Fprintf(&b, "- **Status:** %s\n", status)
	fmt.Fprintf(&b, "- **Passed:** %d / %d\n", r.Passed, r.Total)
	fmt.Fprintf(&b, "- **Failed:** %d / %d\n", r.Failed, r.Total)
	fmt.Fprintf(&b, "- **Run At:** %s\n", r.CreatedAt)
	if r.DryRun {
		fmt.Fprintf(&b, "- **Mode:** Dry Run\n")
	}

	if len(r.Cases) > 0 {
		fmt.Fprintf(&b, "\n### Case Results\n\n")
		fmt.Fprintf(&b, "| Test Case | Status | Failures |\n")
		fmt.Fprintf(&b, "|-----------|--------|----------|\n")
		for _, c := range r.Cases {
			caseStatus := "PASS"
			if !c.Passed {
				caseStatus = "FAIL"
			}
			failureText := "—"
			if len(c.Reasons) > 0 {
				failureText = strings.Join(c.Reasons, "; ")
				// Escape pipes to protect the markdown table.
				failureText = strings.ReplaceAll(failureText, "|", "\\|")
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, caseStatus, failureText)
		}
	}

	return b.String()
}
