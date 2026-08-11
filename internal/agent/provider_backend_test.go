package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent/routing"
	"github.com/isguang2024/fast-spider/internal/node"
)

func TestStaticProviderRegistry(t *testing.T) {
	registry := staticProviderRegistry()
	if len(registry.ordered) != 2 {
		t.Fatalf("provider count = %d", len(registry.ordered))
	}
	if registry.ordered[0].ID != "codex" || registry.ordered[1].ID != "claude_code" {
		t.Fatalf("provider order = %#v", registry.ordered)
	}
	for _, id := range []string{"codex", "claude_code"} {
		definition, ok := registry.get(id)
		if !ok || len(definition.SupportedActions) == 0 {
			t.Fatalf("missing static provider %q", id)
		}
	}
	if _, ok := registry.get("dynamic-provider"); ok {
		t.Fatal("registry unexpectedly accepted a dynamic provider")
	}
}

func TestTTLCacheExpirationCloneAndBound(t *testing.T) {
	if cliProbeTTL < 30*time.Second || cliProbeTTL > 60*time.Second {
		t.Fatalf("CLI probe TTL = %s", cliProbeTTL)
	}
	if modelsTTL < 10*time.Second || modelsTTL > 30*time.Second {
		t.Fatalf("models TTL = %s", modelsTTL)
	}
	if routing.DefaultRouteTTL < time.Second || routing.DefaultRouteTTL > 2*time.Second {
		t.Fatalf("route TTL = %s", routing.DefaultRouteTTL)
	}
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	cache := newTTLCache[map[string]any](20*time.Second, 2, cloneAgentMap)
	cache.now = func() time.Time { return now }
	cache.set("a", map[string]any{"nested": map[string]any{"value": "original"}})
	value, ok := cache.get("a")
	if !ok {
		t.Fatal("cache miss")
	}
	value["nested"].(map[string]any)["value"] = "changed"
	again, _ := cache.get("a")
	if again["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("cache returned shared mutable state")
	}
	cache.set("b", map[string]any{})
	cache.set("c", map[string]any{})
	if len(cache.entries) != 2 {
		t.Fatalf("cache keys = %d", len(cache.entries))
	}
	now = now.Add(21 * time.Second)
	if _, ok := cache.get("c"); ok {
		t.Fatal("expired entry was returned")
	}
}

func TestAdapterFactCachesIncludeNegativeVersionAndSanitizedAuthFacts(t *testing.T) {
	codex := NewCodexAdapter(nil)
	probeErr := fmt.Errorf("%w: executable not found", node.ErrAgentProviderUnavailable)
	codex.versionCache.set("version", versionProbe{err: probeErr})
	if _, err := codex.Availability(context.Background()); !errors.Is(err, node.ErrAgentProviderUnavailable) {
		t.Fatalf("cached Codex availability error = %v", err)
	}
	codex.modelsCache.set("models", map[string]any{"data": []any{"cached"}})
	if models, err := codex.ListModels(context.Background()); err != nil || len(models["data"].([]any)) != 1 {
		t.Fatalf("cached Codex models = %#v, %v", models, err)
	}

	claude := NewClaudeCodeAdapter(t.TempDir(), nil, nil)
	claude.versionCache.set("version", versionProbe{version: "2.1.207"})
	if version, err := claude.Availability(context.Background()); err != nil || version != "2.1.207" {
		t.Fatalf("cached Claude version = %q, %v", version, err)
	}
	claude.authCache.set("auth", map[string]any{"configured": false, "errorClass": ErrorAuthFailed})
	auth := claude.AuthConfiguration(context.Background())
	if auth["errorClass"] != ErrorAuthFailed {
		t.Fatalf("cached Claude auth = %#v", auth)
	}
}

func TestCodexWarningEventSanitizesUpstreamError(t *testing.T) {
	a := NewCodexAdapter(nil)
	raw := `{"threadId":"thread-1","turnId":"turn-1","message":"unauthorized token=secret-value endpoint=https://private.example/v1"}`
	a.handleNotification("error", []byte(raw))
	a.eventMu.Lock()
	event := a.events[len(a.events)-1]
	a.eventMu.Unlock()
	if event.Text != publicErrorMessage(ErrorAuthFailed) {
		t.Fatalf("event text = %q", event.Text)
	}
	if strings.Contains(event.Text, "secret-value") || strings.Contains(event.Text, "private.example") {
		t.Fatalf("event leaked upstream error: %#v", event)
	}
	if event.Detail["errorClass"] != ErrorAuthFailed {
		t.Fatalf("error class = %v", event.Detail["errorClass"])
	}
}

func TestProviderDiscoveryRunsIndependentReadOnlyProbesInParallel(t *testing.T) {
	var started atomic.Int32
	allStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	probe := func() {
		if started.Add(1) == 5 {
			once.Do(func() { close(allStarted) })
		}
		<-release
	}
	discovery := providerDiscovery{
		codexVersion:  func(context.Context) (string, error) { probe(); return "codex-test", nil },
		claudeVersion: func(context.Context) (string, error) { probe(); return "claude-test", nil },
		claudeAuth:    func(context.Context) map[string]any { probe(); return map[string]any{"configured": true} },
		route: func(_ context.Context, app string) (map[string]any, error) {
			probe()
			return map[string]any{"appType": app, "available": true}, nil
		},
	}
	done := make(chan map[string]any, 1)
	go func() { done <- discoverProviders(context.Background(), staticProviderRegistry(), discovery) }()
	select {
	case <-allStarted:
		close(release)
	case <-time.After(time.Second):
		t.Fatalf("only %d discovery probes started; discovery is not parallel", started.Load())
	}
	select {
	case result := <-done:
		providers, _ := result["providers"].([]any)
		if len(providers) != 2 {
			t.Fatalf("providers = %#v", providers)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel discovery did not finish")
	}
}

func TestRouteDiscoveryErrorIsClassifiedWithoutRawPayload(t *testing.T) {
	raw := errors.New("network connection reset token=secret-value endpoint=https://private.example/v1")
	route := publicRouteDiscovery("codex", nil, raw)
	if route["errorClass"] != ErrorNetworkFailed || route["reason"] != "route_inspection_failed" {
		t.Fatalf("route error = %#v", route)
	}
	serialized := fmt.Sprint(route)
	if strings.Contains(serialized, "secret-value") || strings.Contains(serialized, "private.example") {
		t.Fatalf("route error leaked raw payload: %s", serialized)
	}
}

func TestExecutionErrorClassificationIsStrictAndSanitized(t *testing.T) {
	tests := []struct {
		text string
		want ErrorClass
	}{
		{"authentication failed for credential", ErrorAuthFailed},
		{"HTTP 429 quota exceeded", ErrorRateLimited},
		{"provider unavailable", ErrorProviderUnavailable},
		{"network connection reset", ErrorNetworkFailed},
		{"invalid model selected", ErrorInvalidModel},
		{"runtime unavailable", ErrorRuntimeUnavailable},
		{"route mismatch", ErrorRouteMismatch},
		{"something novel", ErrorUnknown},
	}
	allowed := map[ErrorClass]bool{
		ErrorAuthFailed: true, ErrorRateLimited: true, ErrorProviderUnavailable: true,
		ErrorNetworkFailed: true, ErrorInvalidModel: true, ErrorRuntimeUnavailable: true,
		ErrorRouteMismatch: true, ErrorUnknown: true,
	}
	for _, test := range tests {
		if got := classifyExecutionText(test.text); got != test.want || !allowed[got] {
			t.Errorf("classify(%q) = %q, want %q", test.text, got, test.want)
		}
	}
	raw := "unauthorized token=secret-value endpoint=https://private.example/api"
	err := newExecutionError("codex", "test", raw)
	if err.Class != ErrorAuthFailed {
		t.Fatalf("class = %q", err.Class)
	}
	if public := err.Error(); strings.Contains(public, "secret-value") || strings.Contains(public, "private.example") || strings.Contains(public, raw) {
		t.Fatalf("public error leaked raw upstream data: %s", public)
	}
	if got := classifyExecutionError(fmt.Errorf("%w: unavailable", node.ErrAgentProviderUnavailable)); got != ErrorProviderUnavailable {
		t.Fatalf("wrapped provider error classified as %q", got)
	}
	if got := classifyExecutionError(fmt.Errorf("%w: executable not found", node.ErrAgentProviderUnavailable)); got != ErrorRuntimeUnavailable {
		t.Fatalf("missing executable classified as %q", got)
	}
	if got := classifyExecutionError(errors.New("unrecognized")); got != ErrorUnknown {
		t.Fatalf("unknown error classified as %q", got)
	}
}
