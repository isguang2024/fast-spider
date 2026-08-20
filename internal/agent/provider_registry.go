package agent

type providerDefinition struct {
	ID               string
	Name             string
	ExecutionModes   []string
	CredentialSource string
	SupportedActions []string
}

type providerRegistry struct {
	ordered []providerDefinition
	byID    map[string]providerDefinition
}

func staticProviderRegistry() providerRegistry {
	definitions := []providerDefinition{
		{ID: "codex", Name: "Codex", ExecutionModes: []string{"bridge_owned", "external_app_server"}, CredentialSource: "local_only", SupportedActions: []string{
			"provider.readiness", "models.list", "provider.capabilities", "projects.list", "skills.list", "hooks.list", "permissions.list", "plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list",
			"session.list", "session.get", "session.create", "session.send", "session.steer", "session.respond", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive", "session.unarchive", "session.delete", "session.fork", "session.compact", "session.rollback", "session.goal.get", "session.goal.set", "session.goal.clear", "session.settings.update", "session.review",
		}},
		{ID: "claude_code", Name: "Claude Code", ExecutionModes: []string{"cli_stream_json"}, CredentialSource: "local_or_cc_switch", SupportedActions: []string{
			"models.list", "provider.capabilities", "projects.list", "session.list", "session.get", "session.create", "session.send", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive", "session.unarchive", "session.delete",
		}},
	}
	byID := make(map[string]providerDefinition, len(definitions))
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	return providerRegistry{ordered: definitions, byID: byID}
}

func (r providerRegistry) get(id string) (providerDefinition, bool) {
	value, ok := r.byID[id]
	return value, ok
}
