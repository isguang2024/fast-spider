package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type collaborationSchemaProbe struct {
	t     *testing.T
	sends int
}

func (p *collaborationSchemaProbe) Close(context.Context) error { return nil }
func (p *collaborationSchemaProbe) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "session.create" || action == "session.send" {
		var input agentControlParams
		if err := decodeParams(params, &input); err != nil {
			p.t.Fatalf("Node %s violates real AgentControl schema: %v", action, err)
		}
		if input.Model != "gpt-5-6-thinking" || input.Thinking != "max" {
			p.t.Fatalf("model settings lost: %q/%q", input.Model, input.Thinking)
		}
		p.sends++
		return map[string]any{"sessionId": "cloud-schema-probe"}, nil
	}
	return map[string]any{"prepared": true}, nil
}

func TestCollaborationCloudModelParametersMatchAgentControlSchema(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "send"}[reuse], func(t *testing.T) {
			root := t.TempDir()
			p := &collaborationSchemaProbe{t: t}
			c := node.NewLocalCapabilityClient(node.Config{DataDir: filepath.Join(root, "node"), Agent: p})
			packet := map[string]any{"machineId": "machine-1", "workingDirectory": root, "callbackSessionId": "controller-1", "accessMode": "read_only", "callbackType": "text", "idempotencyKey": "schema-probe-key-0001", "prompt": "Analyze the bounded fixture.", "model": "gpt-5-6-thinking", "thinking": "max"}
			if reuse {
				packet["targetSessionId"] = "cloud-schema-probe"
			}
			identity := map[string]any{"dbPath": filepath.Join(root, "mission.sqlite3"), "missionId": "mission-1", "actorSessionId": "controller-1"}
			init := map[string]any{}
			for k, v := range identity {
				init[k] = v
			}
			init["coordinator"], init["authorityRef"], init["nextAction"], init["dispatchEnabled"], init["continuation"] = "coordinator-1", "test:authorized", "Dispatch fixture", true, map[string]any{"enabled": false}
			init["items"] = []any{map[string]any{"id": "task-1", "kind": "decision", "phase": "ready", "owner": "cloud-owner", "executor": "cloud", "next_action": "Dispatch fixture", "packet": packet}}
			call := func(action string, params map[string]any) map[string]any {
				r := c.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{RequestId: "test-schema-" + action, Capability: "collaboration.control", Action: action, Params: params})
				if r.Error != nil {
					t.Fatalf("%s: %+v", action, r.Error)
				}
				return r.Result
			}
			call("init", init)
			identity["actorSessionId"], identity["itemId"] = "coordinator-1", "task-1"
			result := call("dispatch", identity)
			if result["phase"] != "active" || p.sends != 1 {
				t.Fatalf("dispatch did not reach the real input boundary once: %+v sends=%d", result, p.sends)
			}
		})
	}
}

func TestAgentControlDecodeFailureIsDefinitiveRejection(t *testing.T) {
	m := &AgentManager{}
	_, err := m.Control(context.Background(), "session.create", map[string]any{"configurationMode": "advanced"})
	var typed node.AgentCapabilityError
	if !errors.As(err, &typed) {
		t.Fatalf("local decode failure was untyped: %v", err)
	}
	code, _, retryable := typed.CapabilityError()
	if code != "INVALID_REQUEST" || retryable {
		t.Fatalf("pre-provider validation became uncertain: %s %t", code, retryable)
	}
}
