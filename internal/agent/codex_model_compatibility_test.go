package agent

import (
	"context"
	"fmt"
	"testing"
)

func TestCodexMaxUltraTurnAndSettingsCompatibility(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			m := New(t.TempDir(), nil)
			defer m.Close(context.Background())
			cwd := t.TempDir()
			turns := 0
			m.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				switch method {
				case "model/list":
					return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
				case "thread/start", "thread/read", "thread/resume":
					return map[string]any{"thread": map[string]any{"id": "thread-test", "cwd": cwd}}, nil
				case "turn/start":
					turns++
					if params["effort"] != effort {
						t.Fatalf("effort dropped: %v", params)
					}
					return map[string]any{"turn": map[string]any{"id": "turn-test"}}, nil
				default:
					return nil, fmt.Errorf("unexpected method %s", method)
				}
			}
			if _, err := m.sessionCreate(context.Background(), agentControlParams{WorkingDirectory: cwd, Prompt: "hello", Thinking: effort, IdempotencyKey: "model-compat-" + effort}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.sessionSend(context.Background(), agentControlParams{SessionID: "thread-send", Prompt: "hello", Thinking: effort}); err != nil {
				t.Fatal(err)
			}
			settings := agentControlParams{Effort: " " + effort + " "}
			if err := m.prepareSettingsInput(context.Background(), &settings); err != nil || settings.Effort != effort {
				t.Fatalf("settings=%v err=%v", settings.Effort, err)
			}
			if turns != 2 {
				t.Fatalf("turns=%d", turns)
			}
		})
	}
	if err := validateTurnInputs(agentControlParams{Prompt: "hello", Thinking: "made-up"}); err == nil {
		t.Fatal("invalid effort accepted")
	}
}

func TestCodexModelsForceReload(t *testing.T) {
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	requests := 0
	m.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		if method != "model/list" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		requests++
		return map[string]any{"data": []any{map[string]any{"id": fmt.Sprintf("model-%d", requests)}}}, nil
	}
	for i, reload := range []bool{false, false, true} {
		result, err := m.Control(context.Background(), "models.list", map[string]any{"providerId": "codex", "forceReload": reload})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 2 {
			want = 2
		}
		models := result["models"].([]map[string]any)
		if requests != want || models[0]["id"] != fmt.Sprintf("model-%d", want) {
			t.Fatalf("reload=%v requests=%d result=%v", reload, requests, result)
		}
	}
}
