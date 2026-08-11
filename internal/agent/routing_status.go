package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

func (m *AgentManager) routingStatus(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if m == nil || m.ccswitch == nil {
		return map[string]any{"available": false, "source": "cc_switch"}, nil
	}
	appType := strings.TrimSpace(input.AppType)
	if appType != "" {
		if !stringInSet(appType, "claude", "codex", "claude-desktop") {
			return nil, fmt.Errorf("appType must be claude, codex, or claude-desktop")
		}
		route, err := m.ccswitch.InspectApp(ctx, appType)
		if err != nil {
			return map[string]any{"available": false, "source": "cc_switch_db", "reason": "route_inspection_failed", "errorClass": classifyExecutionError(err)}, nil
		}
		return map[string]any{"available": route["available"], "route": route, "source": "cc_switch_db", "authoritative": true}, nil
	}
	apps := []string{"claude", "codex", "claude-desktop"}
	routes := make([]map[string]any, len(apps))
	var wait sync.WaitGroup
	for index, app := range apps {
		index, app := index, app
		wait.Add(1)
		go func() {
			defer wait.Done()
			route, err := m.ccswitch.InspectApp(ctx, app)
			if err != nil {
				routes[index] = map[string]any{"appType": app, "available": false, "reason": "route_inspection_failed", "errorClass": classifyExecutionError(err)}
				return
			}
			routes[index] = route
		}()
	}
	wait.Wait()
	available := false
	for _, route := range routes {
		if value, _ := route["available"].(bool); value {
			available = true
		}
	}
	return map[string]any{"available": available, "routes": routes, "source": "cc_switch_db", "authoritative": true}, nil
}
