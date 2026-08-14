package server

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

func TestMCPGuideCatalogMatchesRegisteredToolsAndDocumentation(t *testing.T) {
	guideNames := mcpRegisteredGuideNames()
	if len(guideNames) != 17 {
		t.Fatalf("guide tool count=%d names=%v", len(guideNames), guideNames)
	}
	var discoveryMarkers []string
	for name, entry := range mcpToolGuides {
		if strings.Contains(strings.ToLower(entry.Description), "fsprobe") {
			discoveryMarkers = append(discoveryMarkers, name)
		}
	}
	if len(discoveryMarkers) != 1 || discoveryMarkers[0] != "machine_list" {
		t.Fatalf("fsprobe must identify only machine_list: %v", discoveryMarkers)
	}
	registeredNames := make([]string, 0, len(guideNames))
	for _, name := range guideNames {
		registeredNames = append(registeredNames, mcpToolDefinition(name, toolAnnotations(true, false, true, false)).Name)
	}
	sort.Strings(registeredNames)
	if strings.Join(registeredNames, "\n") != strings.Join(guideNames, "\n") {
		t.Fatalf("registered=%v guides=%v", registeredNames, guideNames)
	}
	raw, err := os.ReadFile("../../../docs/10-public-api-and-mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	docNames := documentedMCPToolNames(t, string(raw))
	sort.Strings(docNames)
	if strings.Join(docNames, "\n") != strings.Join(guideNames, "\n") {
		t.Fatalf("documented=%v guides=%v", docNames, guideNames)
	}
}

func TestMCPGuideViewsAreCompleteAndBounded(t *testing.T) {
	overview, err := newMCPGuide("0.4.16", "overview", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Categories) != 9 || len(overview.GoldenRules) == 0 || len(overview.RecommendedNext) == 0 {
		t.Fatalf("overview incomplete: %+v", overview)
	}
	assertMCPGuideSize(t, overview, 8<<10)

	for _, name := range mcpRegisteredGuideNames() {
		guide, err := newMCPGuide("0.4.16", "tool", name)
		if err != nil {
			t.Fatalf("tool %s: %v", name, err)
		}
		if guide.Summary == "" || len(guide.WhenToUse) == 0 || len(guide.RequiredInputs) == 0 || len(guide.SafeSequence) == 0 || len(guide.Returns) == 0 || len(guide.RecommendedNext) == 0 || len(guide.CommonErrors) == 0 || len(guide.BoundedExamples) == 0 {
			t.Fatalf("tool guide %s incomplete: %+v", name, guide)
		}
		assertMCPGuideSize(t, guide, 12<<10)
	}
	for _, name := range []string{"connection-check", "file-edit", "shell-job", "build-job", "git-change", "browser", "codex-session", "long-task", "artifact-display"} {
		guide, err := newMCPGuide("0.4.17", "workflow", name)
		if err != nil || guide.Summary == "" || len(guide.SafeSequence) == 0 {
			t.Fatalf("workflow %s guide=%+v err=%v", name, guide, err)
		}
		if name == "connection-check" {
			sequence := strings.Join(guide.SafeSequence, "\n")
			for _, needle := range []string{"api_tool.list_resources", `query="fsprobe"`, "Never materialize the full 17-tool schema", "login/reauthorization", "machine_list"} {
				if !strings.Contains(sequence, needle) {
					t.Fatalf("connection recovery workflow missing %q: %+v", needle, guide)
				}
			}
		}
		assertMCPGuideSize(t, guide, 12<<10)
	}
	for _, name := range []string{"CONNECTION_LOST", "MACHINE_OFFLINE", "DEADLINE_EXCEEDED", "ABSOLUTE_PATH_REQUIRED", "BROWSER_REF_STALE", "NODE_UPDATING", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE", "JOB_NOT_FOUND", "INVALID_REQUEST"} {
		guide, err := newMCPGuide("0.4.16", "error", name)
		if err != nil || guide.Summary == "" || len(guide.SafeSequence) == 0 {
			t.Fatalf("error %s guide=%+v err=%v", name, guide, err)
		}
		assertMCPGuideSize(t, guide, 12<<10)
	}
	for _, input := range []struct{ view, name string }{{"unknown", ""}, {"tool", ""}, {"workflow", ""}, {"error", ""}, {"tool", "unknown"}, {"workflow", "unknown"}, {"error", "UNKNOWN"}} {
		if _, err := newMCPGuide("0.4.16", input.view, input.name); err == nil {
			t.Fatalf("view=%q name=%q unexpectedly accepted", input.view, input.name)
		}
	}
}

func TestMCPServerInstructionsStayBoundedAndCoverCapabilityMap(t *testing.T) {
	if len([]byte(mcpServerInstructions)) > 2<<10 {
		t.Fatalf("instructions size=%d", len([]byte(mcpServerInstructions)))
	}
	for _, needle := range []string{
		"@FastSpider_FS", "capability_list", "machine_list", "machine_get", "file_read", "file_edit", "code_search",
		"shell_run", "build_control", "job_watch", "job_cancel", "git_control", "browser_control", "screenshot_take",
		"ai_control", "working_context", "thinking_team", "artifact_get", "session.list", "view=tool|workflow|error",
		`query="fsprobe"`, "Never load all 17 schemas",
	} {
		if !strings.Contains(mcpServerInstructions, needle) {
			t.Fatalf("instructions missing %q", needle)
		}
	}
}

func assertMCPGuideSize(t *testing.T, guide *mcpGuide, limit int) {
	t.Helper()
	raw, err := json.Marshal(guide)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > limit {
		t.Fatalf("guide %s/%s size=%d limit=%d", guide.View, guide.Name, len(raw), limit)
	}
}

func documentedMCPToolNames(t *testing.T, document string) []string {
	t.Helper()
	anchor := strings.Index(document, "当前固定 17 个工具")
	if anchor < 0 {
		t.Fatal("MCP tool-list anchor missing from documentation")
	}
	blockStart := strings.Index(document[anchor:], "```text\n")
	if blockStart < 0 {
		t.Fatal("MCP tool-list block missing")
	}
	blockStart += anchor + len("```text\n")
	blockEnd := strings.Index(document[blockStart:], "```")
	if blockEnd < 0 {
		t.Fatal("MCP tool-list block is not closed")
	}
	lines := strings.Split(strings.TrimSpace(document[blockStart:blockStart+blockEnd]), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if name := strings.TrimSpace(line); name != "" {
			out = append(out, name)
		}
	}
	return out
}
