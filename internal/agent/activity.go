package agent

func (m *AgentManager) BusyForUpdate() bool {
	if m == nil {
		return false
	}
	return m.codexHasActiveTurn() || m.claudeHasActiveRun()
}

func (m *AgentManager) codexHasActiveTurn() bool {
	if m.codex == nil {
		return false
	}
	m.codex.eventMu.Lock()
	defer m.codex.eventMu.Unlock()
	return len(m.codex.activeTurns) > 0
}

func (m *AgentManager) claudeHasActiveRun() bool {
	if m.claude == nil {
		return false
	}
	m.claude.mu.Lock()
	defer m.claude.mu.Unlock()
	return len(m.claude.active) > 0
}
