package node

import (
	"context"
	"fmt"
	"os"
)

// WorkingMarkdownFolder resolves the bound Markdown workspace for trusted
// loopback UI integrations. It shares the same plan storage and path checks as
// the working.context capability and never accepts a standalone folder path.
func (c *Client) WorkingMarkdownFolder(ctx context.Context, projectPath, planID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	projectPath, err := resolveWorkingContextProject(projectPath)
	if err != nil {
		return "", err
	}
	planID, err = normalizeWorkingPlanID(planID)
	if err != nil {
		return "", err
	}
	state, _, exists, err := loadWorkingContext(c.workingContextPathForPlan(projectPath, planID), projectPath)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", os.ErrNotExist
	}
	if state.PlanID != planID {
		return "", fmt.Errorf("stored working plan does not match planId")
	}
	return resolveWorkingMarkdownRoot(projectPath, state.MarkdownRoot, false)
}
