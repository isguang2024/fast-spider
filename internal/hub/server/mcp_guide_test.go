package server

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestMCPGuideCatalogMatchesRegisteredToolsAndDocumentation(t *testing.T) {
	guideNames := mcpRegisteredGuideNames()
	if len(guideNames) != 22 {
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
	if len(overview.Categories) != 11 || len(overview.ToolSummaries) != len(mcpToolGuides) || len(overview.GoldenRules) == 0 || len(overview.RecommendedNext) == 0 {
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
		if name == "ai_control" {
			guideText := strings.Join(append(append(append(guide.WhenToUse, guide.RequiredInputs...), guide.SafeSequence...), guide.Returns...), "\n")
			for _, needle := range []string{"session.create", "providerId=codex", "backend=chatgpt_cloud", "mode=quick_chat", "CHAT", "externalIdType=chatgpt_conversation", "completionPending=true", "pluginName", "UNSUPPORTED_SESSION_PLUGIN_BINDING", "resultMode=manifest", "session.callback.claim", "about every 30 seconds", "about every 10 minutes", "metadataOnly=true", "exact Codex or ChatGPT sessionId", "optional", "do not call session.list", "AGENT_SESSION_BUSY", "plan.init", "initializeMarkdown=true", "docs/progress/04-open-issues.md"} {
				if !strings.Contains(guideText, needle) {
					t.Fatalf("ai_control guide missing %q: %+v", needle, guide)
				}
			}
			if strings.Contains(guide.Summary, "explicitly unsupported") {
				t.Fatalf("ai_control guide still advertises ChatGPT cloud as unsupported: %s", guide.Summary)
			}
		}
		if name == "codex_cloud_collaboration" {
			guideText := strings.Join(append(append(append([]string{guide.Summary}, guide.RequiredInputs...), guide.SafeSequence...), guide.Returns...), "\n")
			for _, needle := range []string{"targetSessionId", "original creator", "mode=quick_chat", "never call session.list", "per-task lease", "actorSessionId=$self", "codex_cloud_completion", "notify", "Node session.callback", "status.poll", "chat.continue", "controller decision", "released", "plan.init", "initializeMarkdown=true", "docs/progress/04-open-issues.md"} {
				if !strings.Contains(guideText, needle) {
					t.Fatalf("codex_cloud_collaboration guide missing %q: %+v", needle, guide)
				}
			}
		}
		if name == "codex_cloud_completion" {
			guideText := strings.Join(append(append(append([]string{guide.Summary}, guide.RequiredInputs...), guide.SafeSequence...), guide.Returns...), "\n")
			for _, needle := range []string{"notification", "actorSessionId=$self", "64", "five-minute", "verifies", "ack", "Node fallback", "result bodies"} {
				if !strings.Contains(guideText, needle) {
					t.Fatalf("codex_cloud_completion guide missing %q: %+v", needle, guide)
				}
			}
		}
		if name == "browser_control" {
			guideText := strings.Join(append([]string{guide.Summary}, guide.SafeSequence...), "\n")
			for _, needle := range []string{"one session", "finally/defer", "close", "session directory"} {
				if !strings.Contains(guideText, needle) {
					t.Fatalf("browser_control guide missing %q: %+v", needle, guide)
				}
			}
		}
		if name == "build_control" {
			guideText := strings.Join(append([]string{guide.Summary}, guide.SafeSequence...), "\n")
			for _, needle := range []string{"temporary", "compiled test binary", "success/failure/cancel"} {
				if !strings.Contains(guideText, needle) {
					t.Fatalf("build_control guide missing %q: %+v", needle, guide)
				}
			}
		}
	}
	for _, name := range []string{"connection-check", "file-edit", "shell-job", "build-job", "git-change", "browser", "codex-session", "long-task", "artifact-display", "codex-cloud-collaboration"} {
		guide, err := newMCPGuide("0.4.17", "workflow", name)
		if err != nil || guide.Summary == "" || len(guide.SafeSequence) == 0 {
			t.Fatalf("workflow %s guide=%+v err=%v", name, guide, err)
		}
		if name == "connection-check" {
			sequence := strings.Join(guide.SafeSequence, "\n")
			for _, needle := range []string{"api_tool.list_resources", `query="fsprobe"`, "Never materialize the full 19-tool schema", "login/reauthorization", "machine_list"} {
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
	for _, input := range []struct{ view, name string }{{"unknown", ""}, {"capability", ""}, {"tool", ""}, {"workflow", ""}, {"error", ""}, {"tool", "unknown"}, {"workflow", "unknown"}, {"error", "UNKNOWN"}} {
		if _, err := newMCPGuide("0.4.16", input.view, input.name); err == nil {
			t.Fatalf("view=%q name=%q unexpectedly accepted", input.view, input.name)
		}
	}
}

func TestMCPToolSummaryCatalogHasNoGaps(t *testing.T) {
	if len(mcpToolSummaryDefinitions) != len(mcpToolGuides) {
		t.Fatalf("tool summary count=%d guide count=%d", len(mcpToolSummaryDefinitions), len(mcpToolGuides))
	}
	seen := make(map[string]bool, len(mcpToolSummaryDefinitions))
	for _, summary := range mcpToolSummaryDefinitions {
		if summary.Name == "" || summary.Category == "" || summary.Summary == "" || summary.Guide == "" {
			t.Fatalf("incomplete tool summary: %+v", summary)
		}
		if seen[summary.Name] {
			t.Fatalf("duplicate tool summary: %s", summary.Name)
		}
		seen[summary.Name] = true
		if _, ok := mcpToolGuides[summary.Name]; !ok {
			t.Fatalf("tool summary has no detailed guide: %s", summary.Name)
		}
	}
	for name := range mcpToolGuides {
		if !seen[name] {
			t.Fatalf("tool guide has no overview summary: %s", name)
		}
	}
	overview, err := newMCPGuide("test", "overview", "")
	if err != nil {
		t.Fatal(err)
	}
	categorySeen := make(map[string]bool, len(mcpToolGuides))
	for _, category := range overview.Categories {
		for _, name := range category.Tools {
			if categorySeen[name] {
				t.Fatalf("tool appears in multiple overview categories: %s", name)
			}
			categorySeen[name] = true
			if _, ok := mcpToolGuides[name]; !ok {
				t.Fatalf("overview category references unknown tool: %s", name)
			}
		}
	}
	if len(categorySeen) != len(mcpToolGuides) {
		t.Fatalf("overview category coverage=%d guide count=%d", len(categorySeen), len(mcpToolGuides))
	}
}

func TestMCPLowLevelCapabilitySummaryCatalogHasNoGaps(t *testing.T) {
	capabilities := append([]protocolv1.CapabilityDescriptor(nil), protocolv1.NodeCapabilities...)
	capabilities = append(capabilities, protocolv1.ScreenshotCapability, protocolv1.BrowserCapability)
	summaries := mcpCapabilitySummaries(capabilities)
	if len(summaries) != len(capabilities) {
		t.Fatalf("capability summary count=%d capability count=%d", len(summaries), len(capabilities))
	}
	seen := make(map[string]bool, len(summaries))
	for _, summary := range summaries {
		if summary.CapabilityID == "" || summary.Version == "" || len(summary.Actions) == 0 || summary.Summary == "" || len(summary.MCPTools) == 0 {
			t.Fatalf("incomplete capability summary: %+v", summary)
		}
		for _, tool := range summary.MCPTools {
			if _, ok := mcpToolGuides[tool]; !ok {
				t.Fatalf("capability %s maps to unknown MCP tool %s", summary.CapabilityID, tool)
			}
		}
		if seen[summary.CapabilityID] {
			t.Fatalf("duplicate capability summary: %s", summary.CapabilityID)
		}
		seen[summary.CapabilityID] = true
	}
	for _, capability := range capabilities {
		if !seen[capability.CapabilityId] {
			t.Fatalf("capability has no summary: %s", capability.CapabilityId)
		}
		if strings.TrimSpace(mcpCapabilitySummaryByID[capability.CapabilityId]) == "" {
			t.Fatalf("capability summary source missing: %s", capability.CapabilityId)
		}
	}
	for capabilityID := range mcpCapabilitySummaryByID {
		if !seen[capabilityID] {
			t.Fatalf("summary exists for capability not in catalog: %s", capabilityID)
		}
	}
	for capabilityID := range mcpCapabilityMCPToolsByID {
		if !seen[capabilityID] {
			t.Fatalf("MCP mapping exists for capability not in catalog: %s", capabilityID)
		}
	}
}

func TestMCPCapabilityGuidesResolveEveryCatalogEntry(t *testing.T) {
	capabilities := append([]protocolv1.CapabilityDescriptor(nil), protocolv1.NodeCapabilities...)
	capabilities = append(capabilities, protocolv1.ScreenshotCapability, protocolv1.BrowserCapability)
	for _, capability := range capabilities {
		guide, err := newMCPCapabilityGuide("test", capabilities, capability.CapabilityId)
		if err != nil {
			t.Fatalf("capability %s: %v", capability.CapabilityId, err)
		}
		if guide.View != "capability" || guide.Name != capability.CapabilityId || guide.Capability == nil || len(guide.Capability.Actions) == 0 || len(guide.Capability.MCPTools) == 0 {
			t.Fatalf("incomplete capability guide for %s: %+v", capability.CapabilityId, guide)
		}
		assertMCPGuideSize(t, guide, 12<<10)
	}
	for _, input := range []string{"", "unknown"} {
		if _, err := newMCPCapabilityGuide("test", capabilities, input); err == nil {
			t.Fatalf("capability %q unexpectedly accepted", input)
		}
	}
}

func TestMCPServerInstructionsStayBoundedAndCoverCapabilityMap(t *testing.T) {
	if len([]byte(mcpServerInstructions)) > 2<<10 {
		t.Fatalf("instructions size=%d", len([]byte(mcpServerInstructions)))
	}
	for _, needle := range []string{
		"@FastSpider_FS", "capability_list", "machine_list", "machine_get", "audit_log", "operation_log", "file_read", "file_edit", "code_search",
		"shell_run", "build_control", "job_watch", "job_cancel", "git_control", "browser_control", "screenshot_take",
		"ai_control", "codex_cloud_collaboration", "codex_cloud_completion", "working_context", "thinking_team", "artifact_get", "session.list", "view=tool|workflow|error", "view=capability",
		`query="fsprobe"`, "Never load all 22 schemas", "powershell.exe", "tzutil /g", "not a separate PowerShell tool", "backend=chatgpt_cloud", "ChatGPT CHAT",
		"desktopBridge", "nativeConversationStreaming=unsupported", "Desktop owner/control bridge", "Cloud CHAT is optional assistance", "exact Codex or ChatGPT sessionId", "metadataOnly=true", "never list, search, or guess an old one", "targetSessionId", "omission creates a new quick_chat", "claims up to 64", "Node callback delivery",
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
	anchor := strings.Index(document, "当前固定 22 个工具")
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
