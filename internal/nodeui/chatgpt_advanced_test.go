package nodeui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isguang2024/fast-spider/internal/agent"
)

type chatGPTAdvancedTestAgent struct{ catalogErr error }

func (a chatGPTAdvancedTestAgent) Control(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "models.list" {
		return map[string]any{}, nil
	}
	if a.catalogErr != nil {
		return nil, a.catalogErr
	}
	return map[string]any{"models": []map[string]any{{"id": "gpt-5-6-thinking", "title": "GPT-5.6 Thinking"}}, "creationModes": []map[string]any{{"id": "quick_chat"}, {"id": "complete"}}, "defaultModel": "gpt-5-6-thinking", "thinkingOptions": []agent.ChatGPTThinkingOption{
		{ID: "auto", Title: "Auto", Value: "", Source: "local_default"},
		{ID: "standard", Title: "Medium", Value: "standard", Source: "chatgpt_cloud"},
		{ID: "extended", Title: "High", Value: "extended", Source: "chatgpt_cloud"},
		{ID: "max", Title: "Extra High", Value: "max", Source: "chatgpt_cloud"},
	}}, nil
}

func (chatGPTAdvancedTestAgent) Close(context.Context) error { return nil }

func TestLocalUIManagesChatGPTAdvancedModelsInNodeDataDir(t *testing.T) {
	dataDir := t.TempDir()
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.agentController = chatGPTAdvancedTestAgent{}
	handler := app.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/chatgpt-advanced-models", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	cfg := agent.ChatGPTAdvancedConfig{Version: 1, Models: []agent.ChatGPTAdvancedModel{{
		ID: "gpt-5.6-terra-wm", Title: "GPT-5.6 Terra", Thinking: []string{"auto", "extended"}, CustomThinking: []string{"model-specific"},
	}}}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/api/chatgpt-advanced-models", bytes.NewReader(body))
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, post)
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	loaded, err := agent.LoadChatGPTAdvancedConfig(dataDir)
	if err != nil || len(loaded.Models) != 1 || loaded.Models[0].ID != "gpt-5.6-terra-wm" || len(loaded.Models[0].CustomThinking) != 1 || loaded.Models[0].CustomThinking[0] != "model-specific" {
		t.Fatalf("saved config=%+v err=%v", loaded, err)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/chatgpt-advanced-models", nil)
	get.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	if response.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
	var result chatGPTAdvancedResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.ThinkingOptions) != 4 || result.ThinkingOptions[2].ID != "extended" || len(result.Models) != 1 || len(result.Models[0].CustomThinking) != 1 || result.Models[0].CustomThinking[0] != "model-specific" || len(result.LiveModels) != 1 || result.LiveModels[0]["id"] != "gpt-5-6-thinking" || len(result.CreationModes) != 2 || result.ConfigFile == "" {
		t.Fatalf("advanced response=%+v", result)
	}
}

func TestLocalUIRejectsStaleChatGPTThinkingOption(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.agentController = chatGPTAdvancedTestAgent{}
	body := []byte(`{"version":1,"models":[{"id":"model","title":"Model","thinking":["obsolete"]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chatgpt-advanced-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("stale thinking status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLocalUIContainsChatGPTAdvancedEditor(t *testing.T) {
	for _, needle := range []string{"ChatGPT Cloud Advanced", `id="chatgpt-advanced-form"`, "/api/chatgpt-advanced-models", "Quick chat 与等待首个回答", `id="config-chatgpt-configuration-mode"`, "Preset · ChatGPT 官方", "Advanced · 本机配置", `id="config-chatgpt-mode"`, `id="config-chatgpt-model"`, `id="config-chatgpt-thinking"`, "GPT-5.6 Thinking", "预设值", "自定义值", "advanced-custom-thinking", "续聊仍继承原会话"} {
		if !bytes.Contains([]byte(localUIHTML), []byte(needle)) {
			t.Fatalf("local UI missing %q", needle)
		}
	}
}

type catalogPublicTestError struct{ code string }

func (e catalogPublicTestError) Error() string { return "private-token-or-url" }
func (e catalogPublicTestError) CapabilityError() (string, string, bool) {
	return e.code, e.Error(), false
}

func TestChatGPTCatalogErrorDoesNotHideStageOrSaveOnFailure(t *testing.T) {
	for _, catalogErr := range []error{catalogPublicTestError{"CHATGPT_CLOUD_MODELS_FORBIDDEN"}, errors.New("private-token-or-url")} {
		app, err := New(Options{DataDir: t.TempDir(), Version: "test", MachineName: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		app.agentController = chatGPTAdvancedTestAgent{catalogErr: catalogErr}
		original := agent.ChatGPTAdvancedConfig{Version: 1, Models: []agent.ChatGPTAdvancedModel{{ID: "original", Title: "Original", Thinking: []string{"auto"}}}}
		if err := agent.SaveChatGPTAdvancedConfig(app.opts.DataDir, original); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, "/api/chatgpt-advanced-models", bytes.NewBufferString(`{"version":1,"models":[]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
			response := httptest.NewRecorder()
			app.handler().ServeHTTP(response, req)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := "CHATGPT_CLOUD_CATALOG_UNAVAILABLE"
			if public, ok := catalogErr.(catalogPublicTestError); ok {
				want = public.code
			}
			if response.Code != 502 || body["code"] != want || bytes.Contains(response.Body.Bytes(), []byte("private-")) {
				t.Fatalf("unsafe or unclassified UI response: %s", response.Body.String())
			}
		}
		saved, err := agent.LoadChatGPTAdvancedConfig(app.opts.DataDir)
		if err != nil || len(saved.Models) != 1 || saved.Models[0].ID != "original" {
			t.Fatalf("failed catalog lookup changed saved models: %+v %v", saved, err)
		}
	}
}
