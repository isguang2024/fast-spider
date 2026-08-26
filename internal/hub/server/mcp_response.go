package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpDiagnosticsMetaKey = "fastSpider/diagnostics"

// MCPResponseOptions keeps transport diagnostics out of the model-visible
// result by default. Full detail remains available for explicit investigations.
type MCPResponseOptions struct {
	Diagnostics bool `json:"diagnostics,omitempty" jsonschema:"show timing/trace"`
}

func (o MCPResponseOptions) mcpDiagnosticsRequested() bool { return o.Diagnostics }

type mcpResponseOptioner interface {
	mcpDiagnosticsRequested() bool
}

func executeMCPTool[T any, I mcpResponseOptioner](executor *toolExecutor, ctx context.Context, ownerID, tool string, input I) (*mcp.CallToolResult, T, error) {
	var zero T
	out, err := executeTypedTool[T](executor, ctx, ownerID, tool, input)
	if err != nil {
		return nil, zero, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{}}
	diagnostics := projectMCPDiagnostics(&out, !input.mcpDiagnosticsRequested())
	if len(diagnostics) > 0 {
		result.Meta = mcp.Meta{mcpDiagnosticsMetaKey: diagnostics}
	}
	return result, out, nil
}

func executeMCPStructuredTool[T any, I any](executor *toolExecutor, ctx context.Context, ownerID, tool string, input I) (*mcp.CallToolResult, T, error) {
	var zero T
	out, err := executeTypedTool[T](executor, ctx, ownerID, tool, input)
	if err != nil {
		return nil, zero, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{}}, out, nil
}

func projectMCPDiagnostics(output any, compact bool) map[string]any {
	diagnostics := map[string]any{}
	switch out := output.(type) {
	case *fileReadOutput:
		addMCPStringDiagnostic(diagnostics, "requestId", out.RequestID)
		addMCPStringDiagnostic(diagnostics, "traceId", out.TraceID)
		addMCPTimingDiagnostic(diagnostics, out.Timing)
		if compact {
			out.RequestID, out.TraceID, out.Timing = "", "", nil
		}
	case *fileEditOutput:
		addMCPStringDiagnostic(diagnostics, "requestId", out.RequestID)
		addMCPStringDiagnostic(diagnostics, "traceId", out.TraceID)
		addMCPTimingDiagnostic(diagnostics, out.Timing)
		if compact {
			out.RequestID, out.TraceID, out.Timing = "", "", nil
		}
	case *jobOutput:
		addMCPStringDiagnostic(diagnostics, "requestId", out.RequestID)
		addMCPStringDiagnostic(diagnostics, "traceId", out.TraceID)
		addMCPStringDiagnostic(diagnostics, "callRequestId", out.CallRequestID)
		addMCPStringDiagnostic(diagnostics, "callTraceId", out.CallTraceID)
		addMCPTimingDiagnostic(diagnostics, out.Timing)
		if compact {
			out.RequestID, out.TraceID = "", ""
			out.CallRequestID, out.CallTraceID, out.Timing = "", "", nil
		}
	case *codeSearchOutput:
		addMCPStringDiagnostic(diagnostics, "requestId", out.RequestID)
		addMCPStringDiagnostic(diagnostics, "traceId", out.TraceID)
		addMCPInt64Diagnostic(diagnostics, "primaryElapsedMs", out.PrimaryElapsedMs)
		addMCPInt64Diagnostic(diagnostics, "fallbackElapsedMs", out.FallbackElapsedMs)
		addMCPInt64Diagnostic(diagnostics, "elapsedMs", out.ElapsedMs)
		addMCPTimingDiagnostic(diagnostics, out.Timing)
		if compact {
			out.RequestID, out.TraceID = "", ""
			out.PrimaryElapsedMs, out.FallbackElapsedMs, out.ElapsedMs, out.Timing = nil, nil, nil, nil
		}
	case *genericCapabilityOutput:
		projectMCPMapDiagnostics(out.Result, diagnostics, compact)
	}
	return diagnostics
}

func projectMCPMapDiagnostics(result, diagnostics map[string]any, compact bool) {
	if result == nil {
		return
	}
	projectMCPCallIDs(result, diagnostics, compact)
	moveMCPMapDiagnostic(result, diagnostics, "timing", compact)
	moveMCPMapDiagnostic(result, diagnostics, "elapsedMs", compact)
	moveMCPMapDiagnostic(result, diagnostics, "checkedAt", compact)

	job, _ := result["job"].(map[string]any)
	if job == nil {
		return
	}
	jobDiagnostics := map[string]any{}
	projectMCPCallIDs(job, jobDiagnostics, compact)
	moveMCPMapDiagnostic(job, jobDiagnostics, "timing", compact)
	if len(jobDiagnostics) > 0 {
		diagnostics["job"] = jobDiagnostics
	}
}

func projectMCPCallIDs(result, diagnostics map[string]any, compact bool) {
	if _, hasCallRequestID := result["callRequestId"]; hasCallRequestID {
		moveMCPMapDiagnostic(result, diagnostics, "callRequestId", compact)
	} else {
		moveMCPMapDiagnostic(result, diagnostics, "requestId", compact)
	}
	if _, hasCallTraceID := result["callTraceId"]; hasCallTraceID {
		moveMCPMapDiagnostic(result, diagnostics, "callTraceId", compact)
	} else {
		moveMCPMapDiagnostic(result, diagnostics, "traceId", compact)
	}
}

func moveMCPMapDiagnostic(result, diagnostics map[string]any, key string, compact bool) {
	value, exists := result[key]
	if !exists {
		return
	}
	diagnostics[key] = value
	if compact {
		delete(result, key)
	}
}

func addMCPStringDiagnostic(diagnostics map[string]any, key, value string) {
	if value != "" {
		diagnostics[key] = value
	}
}

func addMCPInt64Diagnostic(diagnostics map[string]any, key string, value *int64) {
	if value != nil {
		diagnostics[key] = *value
	}
}

func addMCPTimingDiagnostic(diagnostics map[string]any, value any) {
	if value != nil {
		diagnostics["timing"] = value
	}
}

func mergeMCPToolResults(primary, diagnostics *mcp.CallToolResult) *mcp.CallToolResult {
	if primary == nil {
		return diagnostics
	}
	if diagnostics != nil && len(diagnostics.Meta) > 0 {
		primary.Meta = diagnostics.Meta
	}
	return primary
}
