package agent

import "testing"

func TestAgentManagerBusyForUpdateTracksActiveRuns(t *testing.T) {
	manager := &AgentManager{codex: NewCodexAdapter(nil), claude: &ClaudeCodeAdapter{active: map[string]*claudeRun{}}}
	if manager.BusyForUpdate() {
		t.Fatal("new manager reported busy")
	}
	manager.codex.eventMu.Lock()
	manager.codex.activeTurns["session"] = "turn"
	manager.codex.eventMu.Unlock()
	if !manager.BusyForUpdate() {
		t.Fatal("active Codex turn was not reported busy")
	}
	manager.codex.eventMu.Lock()
	delete(manager.codex.activeTurns, "session")
	manager.codex.eventMu.Unlock()
	manager.claude.mu.Lock()
	manager.claude.active["session"] = &claudeRun{turnID: "turn"}
	manager.claude.mu.Unlock()
	if !manager.BusyForUpdate() {
		t.Fatal("active Claude run was not reported busy")
	}
}
