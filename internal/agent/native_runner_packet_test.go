package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNativeCompilePacketBoundsPlannerHistory(t *testing.T) {
	planner := nativeRunnerTask{ID: "planner-current", ProjectID: "project", Kind: "planner", Title: "Plan", Objective: "advance the goal", Acceptance: "grounded plan", Round: 161, State: "queued"}
	accepted := nativeRunnerTask{
		ID: "accepted", ProjectID: "project", Kind: "work", Title: "Accepted block", State: "accepted",
		Round: 160, AcceptedVersion: "goal-v1", GoalVersion: "goal-v1",
		Validations: map[string]nativeRunnerValidation{"unit": {State: "passed", ExitCode: 0}},
		History:     []nativeRunnerAttempt{{Round: 1, Result: &nativeRunnerResult{Outcome: "completed", Summary: strings.Repeat("old report body ", 220)}}},
	}
	tasks := []nativeRunnerTask{planner, accepted}
	for i := 0; i < 160; i++ {
		historicalPlanner := nativeRunnerTask{
			ID: fmt.Sprintf("planner-history-%03d", i), ProjectID: "project", Kind: "planner",
			Title: "Historical planner", State: "accepted", Round: i + 1, GoalVersion: "goal-v1", AcceptedVersion: "goal-v1",
			Result: &nativeRunnerResult{Outcome: "completed", Path: "planner-result.result", SHA256: "planner-hash"},
			History: []nativeRunnerAttempt{{
				Round: i + 1, GoalVersion: "goal-v1", Correction: strings.Repeat("correction ", 120),
				Result:  &nativeRunnerResult{Outcome: "completed", Summary: strings.Repeat("old report body ", 220), Path: "old-report.result"},
				Request: &nativeRunnerDispatch{Prompt: strings.Repeat("old prompt ", 220), ResultPath: "old-binding.result"},
			}},
		}
		tasks = append(tasks, historicalPlanner)
	}
	packet := nativeCompilePacket(
		nativeRunnerProject{ID: "project", Goal: "retain this complete user goal", GoalVersion: "goal-v1", Revision: 7},
		planner,
		tasks,
		"current.result",
	)
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	base := nativeCompilePacket(
		nativeRunnerProject{ID: "project", Goal: "retain this complete user goal", GoalVersion: "goal-v1", Revision: 7},
		planner,
		[]nativeRunnerTask{planner, accepted},
		"current.result",
	)
	baseRaw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 96<<10 || len(raw)-len(baseRaw) >= 2048 {
		t.Fatalf("bounded packet unexpectedly large or history-sensitive: %d bytes (base %d)", len(raw), len(baseRaw))
	}
	if strings.Contains(string(raw), "old report body") || strings.Contains(string(raw), "old prompt") {
		t.Fatal("historical report or prompt was copied into the packet")
	}
	blocks := packet["blocks"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["id"] != "accepted" {
		t.Fatalf("historical planner was not removed from blocks: %+v", blocks)
	}
	if packet["contextQuery"] == nil || packet["historyPlannerCount"] != 160 {
		t.Fatalf("packet omitted compact context query: %#v", packet["contextQuery"])
	}
	latest := packet["latestAcceptedPlanner"].(map[string]any)
	if latest["resultPath"] != "planner-result.result" || latest["resultSHA256"] != "planner-hash" {
		t.Fatalf("latest accepted planner result index is incomplete: %#v", latest)
	}
	query := packet["contextQuery"].(map[string]any)
	if query["action"] != "runner.context" || query["instruction"] != "Use bound taskRef; select taskId within project; read only needed fields/pages" {
		t.Fatalf("packet context query is not taskRef-bound: %#v", query)
	}
	if got := query["sections"].([]string); len(got) != 4 || got[2] != "history" || got[3] != "evidence" {
		t.Fatalf("packet context query omitted required sections: %#v", query["sections"])
	}
}

func TestNativeCompilePacketPreservesCurrentContractAndEvidence(t *testing.T) {
	task := nativeRunnerTask{
		ID: "current", ProjectID: "project", Parent: "feature", Key: "current-key", Kind: "work", Title: "Current work",
		Objective: "exact objective", Acceptance: "exact acceptance", Scope: "src/owned", Context: []string{"README.md", "src/main.go"},
		Checks: []string{"unit", "integration"}, GoalVersion: "goal-v2", Round: 3, State: "active",
		Request:     &nativeRunnerDispatch{ResultPath: "old-bound-result.result"},
		Result:      &nativeRunnerResult{Outcome: "failed", Path: "old-result.snapshot", SHA256: "old-hash"},
		Validations: map[string]nativeRunnerValidation{"unit": {State: "passed", ExitCode: 0, Evidence: "checks/unit.log"}},
		Recovery:    &nativeRunnerRecovery{Phase: "handover", Checkpoint: nativeRunnerCheckpoint{Summary: "resume from here", NextStep: "run integration", Stage: "context_handover", Evidence: []string{"checks/unit.log"}}},
	}
	packet := nativeCompilePacket(nativeRunnerProject{ID: "project", Goal: "complete user goal", GoalVersion: "goal-v2"}, task, nil, "new-result.result")
	block := packet["taskBlock"].(map[string]any)
	for key, want := range map[string]any{
		"id": task.ID, "projectId": task.ProjectID, "objective": task.Objective, "acceptance": task.Acceptance,
		"scope": task.Scope, "round": task.Round, "state": task.State, "previousResultPath": "old-result.snapshot",
	} {
		if block[key] != want {
			t.Fatalf("taskBlock[%q] = %#v, want %#v", key, block[key], want)
		}
	}
	if got := block["context"].([]string); len(got) != 2 || got[1] != "src/main.go" {
		t.Fatalf("current context was not preserved: %#v", block["context"])
	}
	if got := block["checks"].([]string); len(got) != 2 || got[1] != "integration" {
		t.Fatalf("current checks were not preserved: %#v", block["checks"])
	}
	validations := block["validations"].(map[string]nativeRunnerValidation)
	if validations["unit"].Evidence != "checks/unit.log" {
		t.Fatalf("check evidence was not traceable: %#v", validations)
	}
	result := block["result"].(map[string]any)
	if result["outcome"] != "failed" || result["path"] != "old-result.snapshot" || result["sha256"] != "old-hash" {
		t.Fatalf("current result metadata was not preserved: %#v", result)
	}
	recovery := block["recovery"].(map[string]any)
	checkpoint := recovery["checkpoint"].(nativeRunnerCheckpoint)
	if checkpoint.Summary != "resume from here" || checkpoint.Evidence[0] != "checks/unit.log" {
		t.Fatalf("recovery checkpoint was not preserved: %#v", recovery)
	}
	if _, ok := block["request"]; ok {
		t.Fatal("taskBlock copied the request binding")
	}
	if _, ok := block["history"]; ok {
		t.Fatal("taskBlock copied history")
	}
}
