package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexSessionCreateReturnsOnlyNodeOwnedAppServerMetadata(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	defer manager.Close(context.Background())
	statePath := filepath.Join(dataDir, codexDesktopStateFilename)
	state := []byte(`{"local-projects":{},"thread-project-assignments":{"unrelated":{"projectId":"keep"}}}`)
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	manager.codexStatePath = statePath
	manager.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		switch method {
		case "model/list":
			return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
		case "thread/start":
			return map[string]any{"thread": map[string]any{"id": "thread-create"}}, nil
		case "turn/start":
			return map[string]any{"turn": map[string]any{"id": "turn-create"}}, nil
		default:
			return nil, fmt.Errorf("unexpected Codex method %q", method)
		}
	}
	result, err := manager.sessionCreate(context.Background(), agentControlParams{
		WorkingDirectory: t.TempDir(), Prompt: "hello", IdempotencyKey: "single-path-create-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["sessionId"] != "thread-create" || result["turnId"] != "turn-create" || result["executionMode"] != "codex_app_server" || result["owner"] != "fast_spider_node" {
		t.Fatalf("create result=%#v", result)
	}
	for _, field := range []string{"desktopBridge", "desktopProjectSynced"} {
		if _, exists := result[field]; exists {
			t.Fatalf("create result still contains %s: %#v", field, result)
		}
	}
	gotState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotState, state) {
		t.Fatalf("Codex Desktop state was modified: got=%s want=%s", gotState, state)
	}
}

func TestCodexSessionSendUsesAppServerAndRequiresTurnID(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	var methods []string
	manager.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		methods = append(methods, method)
		switch method {
		case "thread/read":
			if includeTurns, _ := params["includeTurns"].(bool); includeTurns {
				t.Fatal("session.send loaded complete Codex turn history")
			}
			return map[string]any{"thread": map[string]any{"id": "thread-send", "cwd": workingDirectory}}, nil
		case "thread/resume":
			return map[string]any{"thread": map[string]any{"id": "thread-send"}}, nil
		case "turn/start":
			return map[string]any{"turn": map[string]any{}}, nil
		default:
			return nil, fmt.Errorf("unexpected Codex method %q", method)
		}
	}
	_, err := manager.sessionSend(context.Background(), agentControlParams{SessionID: "thread-send", Prompt: "hello"})
	if err == nil || !containsAny(err.Error(), "turnId", "turn ID") {
		t.Fatalf("missing turnId error=%v", err)
	}
	if want := []string{"thread/read", "thread/resume", "turn/start"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods=%#v want=%#v", methods, want)
	}
}

func TestNormalCodexSessionSendDoesNotAutoUnarchive(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	var methods []string
	archivedErr := errors.New("session thread-archived is archived. Run `codex unarchive thread-archived` to unarchive it first")
	manager.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		methods = append(methods, method)
		switch method {
		case "thread/read":
			if includeTurns, _ := params["includeTurns"].(bool); includeTurns {
				t.Fatal("archived session.send loaded complete Codex turn history")
			}
			return map[string]any{"thread": map[string]any{"id": "thread-archived", "cwd": workingDirectory, "archived": true}}, nil
		case "thread/resume":
			return nil, archivedErr
		default:
			return nil, fmt.Errorf("unexpected Codex method %q", method)
		}
	}
	if _, err := manager.sessionSend(context.Background(), agentControlParams{SessionID: "thread-archived", Prompt: "hello"}); !errors.Is(err, archivedErr) {
		t.Fatalf("archived send error=%v", err)
	}
	if want := []string{"thread/read", "thread/resume"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods=%#v want=%#v", methods, want)
	}
}

func TestLocalNotificationUsesConfirmedCallbackDelivery(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			manager := New(t.TempDir(), nil)
			defer manager.Close(context.Background())
			cwd := t.TempDir()
			unarchived := false
			manager.codex.requestOverride = func(_ context.Context, method string, p map[string]any) (map[string]any, error) {
				switch method {
				case "thread/read":
					return map[string]any{"thread": map[string]any{"id": "notification-target", "cwd": cwd}}, nil
				case "thread/resume":
					if !unarchived {
						return nil, errors.New("session notification-target is archived. Run `codex unarchive notification-target` to unarchive it first")
					}
					return map[string]any{"thread": map[string]any{"id": "notification-target"}}, nil
				case "thread/unarchive":
					unarchived = true
					return map[string]any{}, nil
				case "turn/start":
					if p["model"] != nil || p["effort"] != nil {
						t.Fatal("notification changed model configuration")
					}
					turn := map[string]any{}
					if confirmed {
						turn["id"] = "notification-turn"
					}
					return map[string]any{"turn": turn}, nil
				default:
					return nil, fmt.Errorf("unexpected method %s", method)
				}
			}
			result, err := manager.DeliverLocalCodexTurn(context.Background(), "notification-target", "bounded wake")
			if !unarchived {
				t.Fatal("notification did not use confirmed callback policy")
			}
			if confirmed {
				if err != nil || result["turnId"] != "notification-turn" {
					t.Fatalf("result=%v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("unconfirmed delivery accepted")
			}
		})
	}
}

func TestNormalizeCodexExecutionResultRemovesLegacyDesktopFields(t *testing.T) {
	result := map[string]any{
		"executionMode": "codex_desktop_ipc", "owner": "codex_desktop",
		"desktopBridge": map[string]any{"enabled": true}, "desktopProjectSynced": true,
	}
	normalizeCodexExecutionResult(result)
	if result["executionMode"] != "codex_app_server" || result["owner"] != "fast_spider_node" {
		t.Fatalf("normalized result=%#v", result)
	}
	if _, exists := result["desktopBridge"]; exists {
		t.Fatalf("desktopBridge survived normalization: %#v", result)
	}
	if _, exists := result["desktopProjectSynced"]; exists {
		t.Fatalf("desktopProjectSynced survived normalization: %#v", result)
	}
}
