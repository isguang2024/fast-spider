package agent

import (
	"context"
	"net"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCodexDesktopBridgeConfigurationIsExplicit(t *testing.T) {
	for _, test := range []struct {
		value   string
		enabled bool
		wantErr bool
	}{
		{value: "", enabled: runtime.GOOS == "windows"},
		{value: "0"},
		{value: "false"},
		{value: "1", enabled: runtime.GOOS == "windows", wantErr: runtime.GOOS != "windows"},
		{value: "TRUE", enabled: runtime.GOOS == "windows", wantErr: runtime.GOOS != "windows"},
		{value: "sometimes", wantErr: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(codexDesktopBridgeEnv, test.value)
			enabled, err := codexDesktopBridgeConfigured()
			if enabled != test.enabled || (err != nil) != test.wantErr {
				t.Fatalf("configured=(%v, %v) want enabled=%v error=%v", enabled, err, test.enabled, test.wantErr)
			}
		})
	}
}

func TestCodexDesktopBridgeMetadataPublishesDefaultAndLimit(t *testing.T) {
	t.Setenv(codexDesktopBridgeEnv, "")
	adapter := NewCodexAdapter(nil)
	metadata := adapter.desktopBridgeMetadata()
	if metadata["defaultEnabled"] != (runtime.GOOS == "windows") {
		t.Fatalf("defaultEnabled=%v", metadata["defaultEnabled"])
	}
	if metadata["nativeConversationStreaming"] != "unsupported" || metadata["ownership"] != "loaded_local_threads_only" {
		t.Fatalf("desktop bridge metadata=%#v", metadata)
	}
	wantOutbound := "app_server_only"
	if runtime.GOOS == "windows" {
		wantOutbound = "desktop_owner_then_app_server"
	}
	if metadata["outboundTurnRouting"] != wantOutbound {
		t.Fatalf("desktop bridge outbound routing=%#v", metadata)
	}
	if runtime.GOOS == "windows" {
		if metadata["enabled"] != true || metadata["state"] != "waiting_for_harness" {
			t.Fatalf("Windows default metadata=%#v", metadata)
		}
	} else if metadata["enabled"] != false || metadata["state"] != "disabled" {
		t.Fatalf("non-Windows default metadata=%#v", metadata)
	}

	t.Setenv(codexDesktopBridgeEnv, "0")
	disabled := adapter.desktopBridgeMetadata()
	if disabled["enabled"] != false || disabled["configurationSource"] != "environment" {
		t.Fatalf("explicitly disabled metadata=%#v", disabled)
	}
}

func TestCodexDesktopBridgeLocalClientOverrideWinsOverEnvironment(t *testing.T) {
	t.Setenv(codexDesktopBridgeEnv, "1")
	adapter := NewCodexAdapter(nil)
	adapter.SetCodexDesktopBridgeEnabled(false)
	metadata := adapter.desktopBridgeMetadata()
	if metadata["enabled"] != false || metadata["configurationSource"] != "local_client" || metadata["state"] != "disabled" {
		t.Fatalf("local shared mode metadata=%#v", metadata)
	}
	adapter.SetCodexDesktopBridgeEnabled(true)
	metadata = adapter.desktopBridgeMetadata()
	if metadata["enabled"] != true || metadata["configurationSource"] != "local_client" {
		t.Fatalf("local managed mode metadata=%#v", metadata)
	}
}

func TestCodexDesktopBridgeOnlyClaimsLoadedLocalThreads(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-owned"] = struct{}{}
	bridge := &codexDesktopBridge{adapter: adapter}
	params := map[string]any{"hostId": "local", "conversationId": "thread-owned"}

	if !bridge.canHandle("thread-owner-discovery", 1, params) {
		t.Fatal("loaded local thread was not claimed")
	}
	if bridge.canHandle("thread-owner-discovery", 2, params) {
		t.Fatal("unsupported owner-discovery version was claimed")
	}
	if bridge.canHandle("thread-owner-discovery", 1, map[string]any{"hostId": "remote", "conversationId": "thread-owned"}) {
		t.Fatal("remote thread was claimed")
	}
	if bridge.canHandle("thread-owner-discovery", 1, map[string]any{"hostId": "local", "conversationId": "thread-other"}) {
		t.Fatal("unloaded thread was claimed")
	}
	if bridge.canHandle("thread-follower-load-complete-history", 1, params) {
		t.Fatal("unsupported Desktop renderer snapshot request was claimed")
	}

	delete(adapter.loaded, "thread-owned")
	if bridge.canHandle("thread-owner-discovery", 1, params) {
		t.Fatal("released thread remained claimed")
	}
}

func TestCodexDesktopBridgeInterruptVersionCompatibility(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-owned"] = struct{}{}
	bridge := &codexDesktopBridge{adapter: adapter}
	withoutExpected := map[string]any{"hostId": "local", "conversationId": "thread-owned"}
	withExpected := map[string]any{"hostId": "local", "conversationId": "thread-owned", "expectedTurnId": "turn-1"}
	if !bridge.canHandle("thread-follower-interrupt-turn", 3, withoutExpected) {
		t.Fatal("Desktop interrupt v3 compatibility request was rejected")
	}
	if bridge.canHandle("thread-follower-interrupt-turn", 3, withExpected) {
		t.Fatal("Desktop interrupt v3 request with expected turn was accepted")
	}
	if !bridge.canHandle("thread-follower-interrupt-turn", 4, withExpected) {
		t.Fatal("Desktop interrupt v4 request was rejected")
	}
}

func TestCodexDesktopBridgeIPCDiscoveryAndOwnerResponse(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-owned"] = struct{}{}
	bridge := &codexDesktopBridge{adapter: adapter, logger: adapter.logger}
	client, server := net.Pipe()
	defer server.Close()
	serveDone := make(chan error, 1)
	go func() { serveDone <- bridge.serveConnection(client) }()

	initialize, err := readCodexDesktopIPCFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	if initialize.Method != "initialize" || mapString(initialize.Params, "clientType") != "desktop" {
		t.Fatalf("initialize=%#v", initialize)
	}
	if err := writeCodexDesktopIPCFrame(server, new(syncMutex), map[string]any{
		"type":              "response",
		"requestId":         initialize.RequestID,
		"resultType":        "success",
		"method":            "initialize",
		"handledByClientId": "client-fast-spider",
		"result":            map[string]any{"clientId": "client-fast-spider"},
	}); err != nil {
		t.Fatal(err)
	}

	ownerRequest := map[string]any{
		"type":      "request",
		"requestId": "owner-1",
		"version":   1,
		"method":    "thread-owner-discovery",
		"params":    map[string]any{"hostId": "local", "conversationId": "thread-owned"},
	}
	if err := writeCodexDesktopIPCFrame(server, new(syncMutex), map[string]any{
		"type":      "client-discovery-request",
		"requestId": "discovery-1",
		"request":   ownerRequest,
	}); err != nil {
		t.Fatal(err)
	}
	discovery, err := readCodexDesktopIPCFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	if canHandle, _ := discovery.Response["canHandle"].(bool); !canHandle {
		t.Fatalf("owned discovery response=%#v", discovery)
	}

	if err := writeCodexDesktopIPCFrame(server, new(syncMutex), ownerRequest); err != nil {
		t.Fatal(err)
	}
	owner, err := readCodexDesktopIPCFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	if owner.ResultType != "success" || owner.Method != "thread-owner-discovery" || owner.HandledByClientID != "client-fast-spider" {
		t.Fatalf("owner response=%#v", owner)
	}

	_ = server.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Desktop IPC bridge did not stop after disconnect")
	}
}

func TestCodexDesktopFollowerStartTurnRoutesToOwnedAdapter(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-owned"] = struct{}{}
	var gotMethod string
	var gotParams map[string]any
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		gotMethod = method
		gotParams = params
		return map[string]any{"turn": map[string]any{"id": "turn-1"}}, nil
	}
	bridge := &codexDesktopBridge{adapter: adapter}
	result, err := bridge.routeFollowerRequest(context.Background(), "thread-follower-start-turn", map[string]any{
		"conversationId": "thread-owned",
		"turnStart": map[string]any{
			"request": map[string]any{
				"threadId": "thread-owned",
				"input":    []any{map[string]any{"type": "text", "text": "from Desktop"}},
				"cwd":      `V:\workspace`,
				"model":    "gpt-test",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "turn/start" || gotParams["threadId"] != "thread-owned" || gotParams["cwd"] != `V:\workspace` || gotParams["model"] != "gpt-test" {
		t.Fatalf("routed method=%q params=%#v", gotMethod, gotParams)
	}
	want := map[string]any{"method": "thread-follower-start-turn", "result": map[string]any{"result": map[string]any{"turn": map[string]any{"id": "turn-1"}}}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result=%#v want=%#v", result, want)
	}
	if _, err := bridge.routeFollowerRequest(context.Background(), "thread-follower-start-turn", map[string]any{
		"conversationId": "thread-owned",
		"turnStart": map[string]any{"request": map[string]any{
			"threadId": "thread-other",
			"input":    []any{map[string]any{"type": "text", "text": "wrong thread"}},
		}},
	}); err == nil {
		t.Fatal("Desktop follower turn targeting another thread was accepted")
	}
}

// syncMutex is kept local to the test so each simulated Desktop write is a
// complete frame while the bridge exercises its own persistent write lock.
type syncMutex = sync.Mutex
