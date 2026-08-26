package server

import (
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProjectMCPDiagnosticsPreservesBusinessContinuationFields(t *testing.T) {
	result := map[string]any{
		"requestId":     "business-request",
		"callRequestId": "transport-request",
		"sessionId":     "session-1",
		"job": map[string]any{
			"jobId":     "job-1",
			"requestId": "job-transport-request",
			"timing":    map[string]any{"hubTotalMs": float64(7)},
		},
		"timing": map[string]any{"hubTotalMs": float64(9)},
	}
	out := genericCapabilityOutput{Result: result}
	diagnostics := projectMCPDiagnostics(&out, true)

	if out.Result["requestId"] != "business-request" || out.Result["sessionId"] != "session-1" {
		t.Fatalf("business IDs were removed: %#v", out.Result)
	}
	job := out.Result["job"].(map[string]any)
	if job["jobId"] != "job-1" {
		t.Fatalf("jobId was removed: %#v", job)
	}
	for _, key := range []string{"callRequestId", "timing"} {
		if _, exists := out.Result[key]; exists {
			t.Fatalf("compact result retained %s: %#v", key, out.Result)
		}
	}
	for _, key := range []string{"requestId", "timing"} {
		if _, exists := job[key]; exists {
			t.Fatalf("compact job retained %s: %#v", key, job)
		}
	}
	if diagnostics["callRequestId"] != "transport-request" || diagnostics["timing"] == nil || diagnostics["job"] == nil {
		t.Fatalf("diagnostics were not retained in metadata: %#v", diagnostics)
	}
}

func TestProjectMCPDiagnosticsFullKeepsVisibleFields(t *testing.T) {
	elapsed := int64(12)
	timing := &capabilityTiming{HubTotalMs: 13}
	out := codeSearchOutput{RequestID: "request-1", TraceID: "trace-1", ElapsedMs: &elapsed, Timing: timing}
	diagnostics := projectMCPDiagnostics(&out, false)

	if out.RequestID != "request-1" || out.TraceID != "trace-1" || out.ElapsedMs == nil || out.Timing != timing {
		t.Fatalf("full diagnostics changed visible output: %#v", out)
	}
	if diagnostics["requestId"] != "request-1" || diagnostics["elapsedMs"] != elapsed || diagnostics["timing"] != timing {
		t.Fatalf("full diagnostics metadata=%#v", diagnostics)
	}
}

func TestMergeMCPToolResultsPreservesNativeContentAndDiagnostics(t *testing.T) {
	primary := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "artifact"}}}
	diagnostics := &mcp.CallToolResult{Meta: mcp.Meta{mcpDiagnosticsMetaKey: map[string]any{"traceId": "trace-1"}}}
	merged := mergeMCPToolResults(primary, diagnostics)

	if merged != primary || len(merged.Content) != 1 || !reflect.DeepEqual(merged.Meta, diagnostics.Meta) {
		t.Fatalf("merged result=%#v", merged)
	}
}
