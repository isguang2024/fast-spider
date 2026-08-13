package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed thinking_team.json
var thinkingTeamJSON []byte

type thinkingTeamInput struct {
	Action      string   `json:"action" jsonschema:"one of team.get,departments.list,department.get,roles.list,role.get,workflow.get,workspace.get"`
	Department  string   `json:"department,omitempty" jsonschema:"department name for department.get"`
	Role        string   `json:"role,omitempty" jsonschema:"role name for role.get"`
	Workflow    string   `json:"workflow,omitempty" jsonschema:"workflow name for workflow.get: direct,parallel,review,full"`
	Departments []string `json:"departments,omitempty" jsonschema:"optional department names used to filter roles.list"`
}

type thinkingTeamConfig struct {
	Version            string                    `json:"version"`
	ExecutionTarget    string                    `json:"executionTarget"`
	ProviderInvocation bool                      `json:"providerInvocation"`
	Coordinator        map[string]any            `json:"coordinator"`
	Departments        []map[string]any          `json:"departments"`
	Roles              []map[string]any          `json:"roles"`
	RoleInstructions   map[string]string         `json:"roleInstructions"`
	Workflows          map[string]map[string]any `json:"workflows"`
	Workspace          map[string]any            `json:"workspace"`
	SelectionRules     []string                  `json:"selectionRules"`
}

func loadThinkingTeamConfig() (thinkingTeamConfig, error) {
	var cfg thinkingTeamConfig
	if err := json.Unmarshal(thinkingTeamJSON, &cfg); err != nil {
		return thinkingTeamConfig{}, fmt.Errorf("decode embedded thinking team config: %w", err)
	}
	return cfg, nil
}

func thinkingTeamResult(input thinkingTeamInput) (map[string]any, error) {
	cfg, err := loadThinkingTeamConfig()
	if err != nil {
		return nil, err
	}
	action := strings.TrimSpace(input.Action)
	if action == "" {
		action = "team.get"
	}
	base := map[string]any{
		"version":            cfg.Version,
		"executionTarget":    cfg.ExecutionTarget,
		"providerInvocation": cfg.ProviderInvocation,
		"coordinator":        cfg.Coordinator,
	}
	switch action {
	case "team.get":
		base["departments"] = cfg.Departments
		base["roles"] = cfg.Roles
		base["workflows"] = cfg.Workflows
		base["workspace"] = cfg.Workspace
		base["selectionRules"] = cfg.SelectionRules
		return base, nil
	case "departments.list":
		base["departments"] = cfg.Departments
		return base, nil
	case "department.get":
		name := strings.TrimSpace(input.Department)
		if name == "" {
			return nil, fmt.Errorf("department is required for department.get")
		}
		for _, department := range cfg.Departments {
			departmentName, _ := department["name"].(string)
			if departmentName != name {
				continue
			}
			base["department"] = department
			roles := make([]map[string]any, 0)
			for _, role := range cfg.Roles {
				roleDepartment, _ := role["department"].(string)
				if roleDepartment == name {
					roles = append(roles, role)
				}
			}
			base["roles"] = roles
			return base, nil
		}
		return nil, fmt.Errorf("unknown thinking department %q", name)
	case "roles.list":
		wanted := map[string]struct{}{}
		for _, department := range input.Departments {
			department = strings.TrimSpace(department)
			if department != "" {
				wanted[department] = struct{}{}
			}
		}
		roles := make([]map[string]any, 0, len(cfg.Roles))
		for _, role := range cfg.Roles {
			if len(wanted) == 0 {
				roles = append(roles, role)
				continue
			}
			department, _ := role["department"].(string)
			if _, ok := wanted[department]; ok {
				roles = append(roles, role)
			}
		}
		base["roles"] = roles
		return base, nil
	case "role.get":
		name := strings.TrimSpace(input.Role)
		if name == "" {
			return nil, fmt.Errorf("role is required for role.get")
		}
		for _, role := range cfg.Roles {
			roleName, _ := role["name"].(string)
			if roleName == name {
				base["role"] = role
				base["instructions"] = cfg.RoleInstructions[name]
				base["workspace"] = cfg.Workspace
				return base, nil
			}
		}
		return nil, fmt.Errorf("unknown thinking role %q", name)
	case "workflow.get":
		name := strings.TrimSpace(input.Workflow)
		if name == "" {
			name = "parallel"
		}
		workflow, ok := cfg.Workflows[name]
		if !ok {
			return nil, fmt.Errorf("unknown thinking workflow %q", name)
		}
		base["workflowName"] = name
		base["workflow"] = workflow
		base["workspace"] = cfg.Workspace
		base["selectionRules"] = cfg.SelectionRules
		return base, nil
	case "workspace.get":
		base["workspace"] = cfg.Workspace
		return base, nil
	default:
		return nil, fmt.Errorf("unsupported thinking_team action %q", action)
	}
}
