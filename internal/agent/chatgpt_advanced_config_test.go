package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestAdvancedModelCatalogNormalizesKnownTitleWithoutChangingConfiguration(t *testing.T) {
	models := []ChatGPTAdvancedModel{
		{ID: "gpt-5-6-thinking", Title: "GPT-5.6 Sol", Thinking: []string{"auto", "max"}},
		{ID: "gpt-custom", Title: "My custom model", Thinking: []string{"auto"}, CustomThinking: []string{"model-specific"}},
	}
	got := filterChatGPTAdvancedModels(models, []ChatGPTThinkingOption{{ID: "auto"}, {ID: "max"}})
	if got[0].Title != "GPT-5.6 Thinking" || got[1].Title != models[1].Title || !reflect.DeepEqual(got[0].Thinking, models[0].Thinking) || len(got[1].CustomThinking) != 1 || got[1].CustomThinking[0] != "model-specific" || models[0].Title != "GPT-5.6 Sol" {
		t.Fatalf("catalog=%#v original=%#v", got, models)
	}
}

func TestChatGPTAdvancedConfigRoundTripsPrivately(t *testing.T) {
	dataDir := t.TempDir()
	want := ChatGPTAdvancedConfig{Version: 1, Models: []ChatGPTAdvancedModel{{
		ID: " gpt-5.6-terra-wm ", Title: " GPT-5.6 Terra ", Thinking: []string{"auto", "extended", "max"}, CustomThinking: []string{" model-specific "},
	}}}
	if err := SaveChatGPTAdvancedConfig(dataDir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadChatGPTAdvancedConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-5.6-terra-wm" || got.Models[0].Title != "GPT-5.6 Terra" || len(got.Models[0].Thinking) != 3 || len(got.Models[0].CustomThinking) != 1 || got.Models[0].CustomThinking[0] != "model-specific" {
		t.Fatalf("advanced config=%+v", got)
	}
	info, err := os.Stat(filepath.Join(dataDir, ChatGPTAdvancedConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("advanced config permissions=%o, want private", info.Mode().Perm())
	}
}

func TestChatGPTAdvancedConfigRejectsDuplicateModelsAndThinking(t *testing.T) {
	duplicateModel := ChatGPTAdvancedConfig{Version: 1, Models: []ChatGPTAdvancedModel{
		{ID: "same", Title: "One", Thinking: []string{"auto"}},
		{ID: "same", Title: "Two", Thinking: []string{"extended"}},
	}}
	if err := SaveChatGPTAdvancedConfig(t.TempDir(), duplicateModel); err == nil {
		t.Fatal("duplicate advanced model was accepted")
	}
	duplicateThinking := ChatGPTAdvancedConfig{Version: 1, Models: []ChatGPTAdvancedModel{{
		ID: "model", Title: "Model", Thinking: []string{"extended"}, CustomThinking: []string{"extended"},
	}}}
	if err := SaveChatGPTAdvancedConfig(t.TempDir(), duplicateThinking); err == nil {
		t.Fatal("duplicate thinking option was accepted")
	}
}

func TestChatGPTCloudModelsCombinesLiveThinkingWithNodeAdvancedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/models" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"default_model_slug": "gpt-live",
			"models":             []any{map[string]any{"slug": "gpt-live", "title": "Live"}},
			"versions": []any{map[string]any{"id": "live", "intelligence_presets": []any{
				map[string]any{"lane": "thinking", "model_slug": "gpt-live", "selected_display_title": "Medium", "thinking_effort": "standard"},
				map[string]any{"lane": "thinking", "model_slug": "gpt-live", "selected_display_title": "Extra High", "thinking_effort": "max"},
			}}},
		})
	}))
	defer server.Close()
	dataDir := t.TempDir()
	if err := SaveChatGPTAdvancedConfig(dataDir, ChatGPTAdvancedConfig{Version: 1, Models: []ChatGPTAdvancedModel{{
		ID: "gpt-custom", Title: "Custom", Thinking: []string{"auto", "standard", "extended", "max"}, CustomThinking: []string{"model-specific"},
	}}}); err != nil {
		t.Fatal(err)
	}
	manager := New(dataDir, nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("advanced", "quick_chat", "gpt-custom", "max")
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	catalog, err := manager.Control(context.Background(), "models.list", map[string]any{"providerId": "codex", "backend": sessionBackendChatGPTCloud})
	if err != nil {
		t.Fatal(err)
	}
	modes, _ := catalog["configurationModes"].([]map[string]any)
	if len(modes) != 2 || modes[1]["modelSource"] != "advancedModels" || modes[1]["customThinkingSource"] != "advancedModels.customThinking" {
		t.Fatalf("configurationModes=%#v", modes)
	}
	options, _ := catalog["thinkingOptions"].([]ChatGPTThinkingOption)
	if len(options) != 3 || options[1].ID != "standard" || options[2].ID != "max" {
		t.Fatalf("thinkingOptions=%#v", options)
	}
	models, _ := catalog["advancedModels"].([]ChatGPTAdvancedModel)
	if len(models) != 1 || len(models[0].Thinking) != 3 || models[0].Thinking[0] != "auto" || models[0].Thinking[2] != "max" || len(models[0].CustomThinking) != 1 || models[0].CustomThinking[0] != "model-specific" {
		t.Fatalf("advancedModels=%#v", models)
	}
	if _, ok := catalog["models"].([]map[string]any); !ok {
		t.Fatalf("live models missing: %#v", catalog["models"])
	}
	defaults, _ := catalog["localCreateDefaults"].(map[string]any)
	if defaults["configurationMode"] != "advanced" || defaults["mode"] != "quick_chat" || defaults["model"] != "gpt-custom" || defaults["thinking"] != "max" {
		t.Fatalf("localCreateDefaults=%#v", defaults)
	}
}
