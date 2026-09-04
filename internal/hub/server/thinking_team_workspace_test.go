package server

import "testing"

func TestThinkingTeamWorkspaceUsesPlainTextWorkingContext(t *testing.T) {
	result, err := thinkingTeamResult(thinkingTeamInput{Action: "workspace.get"})
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := result["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace=%T", result["workspace"])
	}
	if workspace["storage"] != "working_context" || workspace["format"] != "plain_text" {
		t.Fatalf("workspace=%v", workspace)
	}
	if _, exists := workspace["files"]; exists {
		t.Fatalf("plain-text workspace still exposes managed files: %v", workspace["files"])
	}
	rules, ok := workspace["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("workspace rules=%v", workspace["rules"])
	}
}
