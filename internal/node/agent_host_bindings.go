package node

import (
	"context"
	"fmt"

	"github.com/isguang2024/fast-spider/hostapi"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

// bindAgentHost supplies Node-owned services to an injected agent. New agents
// use the typed public hostapi binder; the legacy setter path keeps the current
// in-repository AgentManager behavior source-compatible during migration.
func (c *Client) bindAgentHost() {
	if c == nil || c.agent == nil {
		return
	}
	machineID := func() (string, error) {
		state, err := c.State()
		if err != nil {
			return "", err
		}
		return state.MachineID, nil
	}
	if binder, ok := c.agent.(hostapi.AgentHostBinder); ok {
		binder.BindHost(hostapi.HostBindings{
			ResultPublisher: c,
			JobExecutor:     NewNativeRunnerJobExecutor(c.jobs),
			MachineID:       machineID,
			Capabilities: hostapi.CapabilityCallFunc(func(ctx context.Context, capability, action string, params map[string]any) (map[string]any, error) {
				response := c.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{Capability: capability, Action: action, Params: params})
				if response.Error != nil {
					return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
				}
				return response.Result, nil
			}),
		})
		return
	}
	if setter, ok := c.agent.(interface{ SetCloudResultPublisher(any) }); ok {
		setter.SetCloudResultPublisher(c)
	}
	if setter, ok := c.agent.(interface{ SetNativeRunnerJobExecutor(any) }); ok {
		setter.SetNativeRunnerJobExecutor(NewNativeRunnerJobExecutor(c.jobs))
	}
	if setter, ok := c.agent.(interface {
		SetNativeRunnerMachineIDProvider(func() (string, error))
	}); ok {
		setter.SetNativeRunnerMachineIDProvider(machineID)
	}
}
