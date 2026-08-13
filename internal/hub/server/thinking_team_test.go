package server

import "testing"

func TestThinkingTeamConfigAndRoleInstructions(t *testing.T) {
	result, err := thinkingTeamResult(thinkingTeamInput{Action: "team.get"})
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := result["providerInvocation"].(bool); enabled {
		t.Fatal("thinking_team must not invoke local AI providers")
	}
	departments, ok := result["departments"].([]map[string]any)
	if !ok || len(departments) != 9 {
		t.Fatalf("departments=%T len=%d want=9", result["departments"], len(departments))
	}
	roles, ok := result["roles"].([]map[string]any)
	if !ok || len(roles) != 17 {
		t.Fatalf("roles=%T len=%d want=17", result["roles"], len(roles))
	}
	cfg, err := loadThinkingTeamConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RoleInstructions) != len(roles) {
		t.Fatalf("roleInstructions=%d roles=%d", len(cfg.RoleInstructions), len(roles))
	}
	for _, item := range roles {
		name, _ := item["name"].(string)
		if cfg.RoleInstructions[name] == "" {
			t.Fatalf("missing instructions for role %q", name)
		}
	}

	role, err := thinkingTeamResult(thinkingTeamInput{Action: "role.get", Role: "总架构师"})
	if err != nil {
		t.Fatal(err)
	}
	instructions, _ := role["instructions"].(string)
	if instructions == "" {
		t.Fatal("总架构师 instructions must not be empty")
	}
	workspace, ok := role["workspace"].(map[string]any)
	if !ok || workspace["storage"] != "working_context" {
		t.Fatalf("workspace=%v", role["workspace"])
	}
}

func TestThinkingTeamDepartmentFilterAndWorkspace(t *testing.T) {
	result, err := thinkingTeamResult(thinkingTeamInput{Action: "roles.list", Departments: []string{"审计部"}})
	if err != nil {
		t.Fatal(err)
	}
	roles, ok := result["roles"].([]map[string]any)
	if !ok || len(roles) != 4 {
		t.Fatalf("audit roles=%T len=%d want=4", result["roles"], len(roles))
	}
	for _, role := range roles {
		if role["department"] != "审计部" {
			t.Fatalf("unexpected audit role=%v", role)
		}
	}

	departmentResult, err := thinkingTeamResult(thinkingTeamInput{Action: "department.get", Department: "技术架构部"})
	if err != nil {
		t.Fatal(err)
	}
	departmentRoles, ok := departmentResult["roles"].([]map[string]any)
	if !ok || len(departmentRoles) != 3 {
		t.Fatalf("architecture roles=%T len=%d want=3", departmentResult["roles"], len(departmentRoles))
	}

	workspaceResult, err := thinkingTeamResult(thinkingTeamInput{Action: "workspace.get"})
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := workspaceResult["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace=%T", workspaceResult["workspace"])
	}
	if workspace["recommendedMarkdownRoot"] != ".local/fast-spider/collaboration/<task-id>" {
		t.Fatalf("recommendedMarkdownRoot=%v", workspace["recommendedMarkdownRoot"])
	}
}

func TestThinkingTeamRejectsUnknownRoleDepartmentAndWorkflow(t *testing.T) {
	if _, err := thinkingTeamResult(thinkingTeamInput{Action: "role.get", Role: "不存在"}); err == nil {
		t.Fatal("expected unknown role error")
	}
	if _, err := thinkingTeamResult(thinkingTeamInput{Action: "department.get", Department: "不存在"}); err == nil {
		t.Fatal("expected unknown department error")
	}
	if _, err := thinkingTeamResult(thinkingTeamInput{Action: "workflow.get", Workflow: "missing"}); err == nil {
		t.Fatal("expected unknown workflow error")
	}
}
