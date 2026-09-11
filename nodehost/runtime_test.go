package nodehost_test

import (
	"context"
	"testing"

	"github.com/isguang2024/fast-spider/hostapi"
	"github.com/isguang2024/fast-spider/nodehost"
)

type bindingAgent struct {
	bindings hostapi.HostBindings
}

func (a *bindingAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (a *bindingAgent) Close(context.Context) error { return nil }

func (a *bindingAgent) BindHost(bindings hostapi.HostBindings) { a.bindings = bindings }

func TestRuntimeBindsExternalAgentThroughPublicContracts(t *testing.T) {
	agent := &bindingAgent{}
	runtime, err := nodehost.NewRuntime(nodehost.RuntimeOptions{
		DataDir:          t.TempDir(),
		Version:          "test",
		Agent:            agent,
		AgentCallerOwned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		t.Fatal("expected runtime")
	}
	if agent.bindings.ResultPublisher == nil {
		t.Fatal("expected result publisher binding")
	}
	if agent.bindings.JobExecutor == nil {
		t.Fatal("expected Job executor binding")
	}
	if agent.bindings.Capabilities == nil {
		t.Fatal("expected capability caller binding")
	}
	if agent.bindings.MachineID == nil {
		t.Fatal("expected machine identity provider binding")
	}
}

func TestDefaultAgentIsAvailableWithoutInternalImports(t *testing.T) {
	agent := nodehost.NewDefaultAgent(t.TempDir(), nil)
	if agent == nil {
		t.Fatal("expected default agent")
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
