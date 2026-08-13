package server

import "testing"

func TestThinkingTeamWorkspaceUsesWorkingContextStandardFiles(t *testing.T) {
	result, err := thinkingTeamResult(thinkingTeamInput{Action: "workspace.get"})
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := result["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace=%T", result["workspace"])
	}
	if initialize, _ := workspace["initializeMarkdown"].(bool); !initialize {
		t.Fatalf("initializeMarkdown=%v want=true", workspace["initializeMarkdown"])
	}
	files, ok := workspace["files"].([]any)
	if !ok || len(files) != 6 || files[0] != "00-current-state.md" || files[3] != "03-acceptance-log.md" {
		t.Fatalf("workspace files=%v", workspace["files"])
	}
	sections, ok := workspace["sections"].(map[string]any)
	if !ok || sections["readEvidence"] != "00-current-state.md#Read Evidence" {
		t.Fatalf("workspace sections=%v", workspace["sections"])
	}
}
