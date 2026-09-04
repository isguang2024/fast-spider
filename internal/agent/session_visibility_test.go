package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boolPointer(value bool) *bool { return &value }

func TestResolveSessionVisibilityMatrix(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		input    agentControlParams
		want     sessionVisibilitySpec
		wantCode string
	}{
		{
			name:     "visible Codex defaults to local target",
			provider: "codex",
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityVisible,
				Backend:          sessionBackendCodexLocal,
				VisibilityTarget: sessionBackendCodexLocal,
				ExternalIDType:   "codex_thread",
			},
		},
		{
			name:     "internal Codex defaults ephemeral and no target",
			provider: "codex",
			input:    agentControlParams{Visibility: sessionVisibilityInternal},
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityInternal,
				Backend:          sessionBackendCodexLocal,
				VisibilityTarget: sessionVisibilityTargetNone,
				Ephemeral:        true,
				ExternalIDType:   "codex_thread",
			},
		},
		{
			name:     "persistent internal Codex is explicitly best effort",
			provider: "codex",
			input:    agentControlParams{Visibility: sessionVisibilityInternal, Ephemeral: boolPointer(false)},
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityInternal,
				Backend:          sessionBackendCodexLocal,
				VisibilityTarget: sessionVisibilityTargetNone,
				Ephemeral:        false,
				ExternalIDType:   "codex_thread",
			},
		},
		{
			name:     "visible Claude local",
			provider: "claude_code",
			input: agentControlParams{
				Backend:          sessionBackendClaudeLocal,
				VisibilityTarget: sessionBackendClaudeLocal,
			},
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityVisible,
				Backend:          sessionBackendClaudeLocal,
				VisibilityTarget: sessionBackendClaudeLocal,
				ExternalIDType:   "claude_session",
			},
		},
		{
			name:     "cloud backend is supported for codex",
			provider: "codex",
			input:    agentControlParams{Backend: sessionBackendChatGPTCloud},
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityVisible,
				Backend:          sessionBackendChatGPTCloud,
				VisibilityTarget: sessionBackendChatGPTCloud,
				ExternalIDType:   "chatgpt_conversation",
			},
		},
		{
			name:     "cloud target is supported for codex",
			provider: "codex",
			input:    agentControlParams{Backend: sessionBackendChatGPTCloud, VisibilityTarget: sessionBackendChatGPTCloud},
			want: sessionVisibilitySpec{
				Visibility:       sessionVisibilityVisible,
				Backend:          sessionBackendChatGPTCloud,
				VisibilityTarget: sessionBackendChatGPTCloud,
				ExternalIDType:   "chatgpt_conversation",
			},
		},
		{
			name:     "cloud requires non-codex provider is rejected",
			provider: "claude_code",
			input:    agentControlParams{Backend: sessionBackendChatGPTCloud},
			wantCode: "AGENT_SESSION_VISIBILITY_INVALID",
		},
		{
			name:     "cloud internal is rejected",
			provider: "codex",
			input:    agentControlParams{Visibility: sessionVisibilityInternal, Backend: sessionBackendChatGPTCloud},
			wantCode: "AGENT_SESSION_VISIBILITY_INVALID",
		},
		{
			name:     "internal cannot publish local target",
			provider: "codex",
			input:    agentControlParams{Visibility: sessionVisibilityInternal, VisibilityTarget: sessionBackendCodexLocal},
			wantCode: "AGENT_SESSION_VISIBILITY_INVALID",
		},
		{
			name:     "visible cannot be ephemeral",
			provider: "codex",
			input:    agentControlParams{Ephemeral: boolPointer(true)},
			wantCode: "AGENT_SESSION_VISIBILITY_INVALID",
		},
		{
			name:     "Claude has no ephemeral create mode",
			provider: "claude_code",
			input:    agentControlParams{Visibility: sessionVisibilityInternal, Ephemeral: boolPointer(true)},
			wantCode: "AGENT_SESSION_VISIBILITY_UNSUPPORTED",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSessionVisibility(test.provider, test.input)
			if test.wantCode != "" {
				if err == nil {
					t.Fatal("resolveSessionVisibility unexpectedly succeeded")
				}
				var capabilityErr interface{ CapabilityError() (string, string, bool) }
				if !errors.As(err, &capabilityErr) {
					t.Fatalf("error does not expose capability code: %v", err)
				}
				code, _, _ := capabilityErr.CapabilityError()
				if code != test.wantCode {
					t.Fatalf("error code=%q want %q", code, test.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSessionVisibility error=%v", err)
			}
			if got.Visibility != test.want.Visibility || got.Backend != test.want.Backend || got.VisibilityTarget != test.want.VisibilityTarget || got.Ephemeral != test.want.Ephemeral || got.ExternalIDType != test.want.ExternalIDType {
				t.Fatalf("spec=%+v want core fields from %+v", got, test.want)
			}
		})
	}
}

func TestSessionVisibilityStorePersistsAndFiltersMetadata(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionVisibilityStore(dataDir)
	spec, err := resolveSessionVisibility("codex", agentControlParams{Visibility: sessionVisibilityInternal, Ephemeral: boolPointer(false)})
	if err != nil {
		t.Fatal(err)
	}
	record := spec.record("codex", "thread-internal", time.Now().UTC())
	if err := store.put(record); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionVisibilityStore(dataDir)
	snapshot, err := reloaded.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := visibilityRecordFor(snapshot, "codex", "thread-internal")
	if got.Visibility != sessionVisibilityInternal || got.VisibilityTarget != sessionVisibilityTargetNone || got.VisibilityGuarantee != "not_guaranteed" {
		t.Fatalf("reloaded visibility=%+v", got)
	}
	visible := map[string]any{"sessionId": "thread-internal"}
	got.applyToResult(visible)
	if visible["externalThreadId"] != "thread-internal" || visible["visibilityNote"] == "" {
		t.Fatalf("decorated session=%#v", visible)
	}
	if err := reloaded.delete("codex", "thread-internal"); err != nil {
		t.Fatal(err)
	}
	if snapshot, err = reloaded.snapshot(); err != nil {
		t.Fatal(err)
	} else if len(snapshot) != 0 {
		t.Fatalf("metadata remained after delete: %#v", snapshot)
	}
}

func TestSessionVisibilityStoreFailsClosedWhenIndexIsCorrupt(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "agent", "session-visibility.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSessionVisibilityStore(dataDir)
	if _, err := store.snapshot(); err == nil {
		t.Fatal("corrupt visibility index was accepted")
	}
}

func TestSessionCreateSpecHashSeparatesVisibilityAndMigratesLegacyDefault(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	oldSpec := map[string]any{"providerId": "codex", "workingDirectory": filepath.Join(dataDir, "project"), "model": "gpt-test", "thinking": "", "prompt": "", "skills": []agentSkillInput(nil), "images": []string(nil), "localImages": []string(nil), "mentions": []agentMentionInput(nil), "imageDetail": "", "outputSchema": map[string]any(nil), "summary": "", "personality": "", "serviceTier": ""}
	legacyHash := sessionCreateSpecHash(oldSpec)
	newSpec := map[string]any{}
	for key, value := range oldSpec {
		newSpec[key] = value
	}
	newSpec["visibilityContractVersion"] = visibilityContractVersion
	newSpec["visibility"] = sessionVisibilityVisible
	newSpec["backend"] = sessionBackendCodexLocal
	newSpec["visibilityTarget"] = sessionBackendCodexLocal
	newSpec["ephemeral"] = false
	newHash := sessionCreateSpecHash(newSpec)
	key := "codex:legacy-key-1234"
	store.records[key] = sessionCreateRecord{Key: key, SpecHash: legacyHash, State: "succeeded", Result: map[string]any{"sessionId": "thread-legacy"}, UpdatedAt: time.Now().UTC()}
	if _, _, err := store.begin(key, newHash, legacyHash); err != nil {
		t.Fatal(err)
	}
	if store.records[key].SpecHash != newHash {
		t.Fatalf("legacy hash was not migrated: %q", store.records[key].SpecHash)
	}
	internalHash := sessionCreateSpecHash(map[string]any{"visibility": sessionVisibilityInternal, "backend": sessionBackendCodexLocal, "visibilityTarget": sessionVisibilityTargetNone, "ephemeral": true})
	if _, _, err := store.begin(key, internalHash); err == nil {
		t.Fatal("different visibility unexpectedly reused idempotency record")
	} else {
		var capabilityErr interface{ CapabilityError() (string, string, bool) }
		if !errors.As(err, &capabilityErr) {
			t.Fatalf("conflict error=%v", err)
		}
		code, _, _ := capabilityErr.CapabilityError()
		if code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("conflict code=%q", code)
		}
	}
}

func TestCodexThreadStartParamsCanRequestEphemeralThread(t *testing.T) {
	params := codexThreadStartParamsWithEphemeral("/work", "/work", "gpt-test", "", true)
	if value, ok := params["ephemeral"].(bool); !ok || !value {
		t.Fatalf("ephemeral parameter=%#v", params["ephemeral"])
	}
}

func TestSessionCreateVisibilityRoundTripAndListFilter(t *testing.T) {
	dataDir := t.TempDir()
	workingDirectory := t.TempDir()
	manager := New(dataDir, nil)
	manager.codexStatePath = filepath.Join(dataDir, "codex-state.json")
	var startedParams map[string]any
	startCount := 0
	manager.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		switch method {
		case "model/list":
			return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
		case "thread/start":
			startCount++
			startedParams = params
			return map[string]any{"thread": map[string]any{"id": "thread-internal"}}, nil
		case "thread/list":
			return map[string]any{"data": []any{
				map[string]any{"id": "thread-internal", "cwd": workingDirectory},
				map[string]any{"id": "thread-visible", "cwd": workingDirectory},
			}}, nil
		default:
			return nil, errors.New("unexpected Codex method " + method)
		}
	}
	result, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "workingDirectory": workingDirectory, "visibility": "internal", "idempotencyKey": "visibility-key-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	if startedParams["ephemeral"] != true {
		t.Fatalf("thread/start params=%#v", startedParams)
	}
	if result["visibility"] != sessionVisibilityInternal || result["visibilityTarget"] != sessionVisibilityTargetNone || result["externalThreadId"] != "thread-internal" || result["ephemeral"] != true {
		t.Fatalf("create result=%#v", result)
	}
	list, err := manager.Control(context.Background(), "session.list", map[string]any{"providerId": "codex", "limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	sessions, _ := list["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["sessionId"] != "thread-visible" || sessions[0]["visibility"] != sessionVisibilityVisible {
		t.Fatalf("filtered sessions=%#v", sessions)
	}
	replayed, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "workingDirectory": workingDirectory, "visibility": "internal", "idempotencyKey": "visibility-key-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	if startCount != 1 || replayed["idempotencyStatus"] != "replayed" || replayed["externalThreadId"] != "thread-internal" || replayed["executionMode"] != "codex_app_server" || replayed["owner"] != "fast_spider_node" {
		t.Fatalf("same-process replay count=%d result=%#v", startCount, replayed)
	}
	restarted := New(dataDir, nil)
	restarted.codexStatePath = filepath.Join(dataDir, "codex-state.json")
	restarted.codex.requestOverride = manager.codex.requestOverride
	restartedReplay, err := restarted.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "workingDirectory": workingDirectory, "visibility": "internal", "idempotencyKey": "visibility-key-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	if startCount != 1 || restartedReplay["idempotencyStatus"] != "replayed" || restartedReplay["visibility"] != sessionVisibilityInternal || restartedReplay["executionMode"] != "codex_app_server" || restartedReplay["owner"] != "fast_spider_node" {
		t.Fatalf("restart replay count=%d result=%#v", startCount, restartedReplay)
	}
	if _, err := restarted.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "workingDirectory": workingDirectory, "visibility": "visible", "idempotencyKey": "visibility-key-1234",
	}); err == nil {
		t.Fatal("different visibility unexpectedly replayed the internal session")
	} else {
		var capabilityErr interface{ CapabilityError() (string, string, bool) }
		if !errors.As(err, &capabilityErr) {
			t.Fatalf("visibility conflict error=%v", err)
		}
		code, _, _ := capabilityErr.CapabilityError()
		if code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("visibility conflict code=%q", code)
		}
	}
}
