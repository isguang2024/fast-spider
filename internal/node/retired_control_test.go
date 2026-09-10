package node

import (
	"context"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestRetiredBusinessControlIsNotRoutable(t *testing.T) {
	client := NewLocalCapabilityClient(Config{DataDir: t.TempDir()})
	for _, action := range []string{"init", "dispatch", "apply", "next_actions", "callback_claim"} {
		response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
			RequestId: "retired-control-" + action, Capability: "collaboration.control", Action: action, Params: map[string]any{},
		})
		if response.Error == nil {
			t.Fatalf("retired action remained callable: %s", action)
		}
	}
}
