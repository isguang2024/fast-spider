package agent

import (
	"context"
	"testing"
)

func TestNativeRunnerControlAcceptsExternalRunnerParameters(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())

	root := t.TempDir()
	result, err := manager.Control(context.Background(), "runner.init", map[string]any{
		"projectId":           "control-project",
		"root":                root,
		"controllerSessionId": "controller-session",
		"goal":                "exercise the external runner control boundary",
		"concurrency":         3,
		"checks": map[string]any{
			"unit": map[string]any{
				"argv":           []any{"go", "test", "./..."},
				"cwd":            ".",
				"timeoutSeconds": 30,
			},
		},
	})
	if err != nil {
		t.Fatalf("runner.init through AgentManager.Control: %v", err)
	}
	project, ok := result["project"].(nativeRunnerProject)
	if !ok || project.ID != "control-project" || project.Concurrency != 3 || project.Checks["unit"].Argv[0] != "go" {
		t.Fatalf("unexpected initialized project: %#v", result["project"])
	}

	status, err := manager.Control(context.Background(), "runner.status", map[string]any{
		"projectId": "control-project",
	})
	if err != nil {
		t.Fatalf("runner.status through AgentManager.Control: %v", err)
	}
	if status["project"] == nil {
		t.Fatalf("runner.status did not return project: %#v", status)
	}
}
