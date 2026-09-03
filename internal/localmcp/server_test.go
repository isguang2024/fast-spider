package localmcp

import (
	"context"
	"io"
	"log/slog"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLocalMCPListsToolsAndRoutesWithoutMachineID(t *testing.T) {
	t.Parallel()
	var got protocolv1.CapabilityRequest
	server := newServer(t.TempDir(), "test", slog.New(slog.NewTextHandler(io.Discard, nil)), func(_ context.Context, _ string, req protocolv1.CapabilityRequest) (protocolv1.CapabilityResponse, error) {
		got = req
		return protocolv1.CapabilityResponse{
			RequestId: "lreq_test",
			TraceId:   "trace_test",
			Result:    map[string]any{"content": "ok"},
		}, nil
	})
	client := connectTestClient(t, server)

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if len(tools.Tools) != 2 || !names["local_machine"] || !names["local_capability"] {
		t.Fatalf("tools=%v", tools.Tools)
	}

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "local_capability",
		Arguments: map[string]any{
			"capability": "file.read",
			"action":     "read",
			"params":     map[string]any{"path": "V:/project/readme.md"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Capability != "file.read" || got.Action != "read" || got.Params["path"] != "V:/project/readme.md" {
		t.Fatalf("request=%+v", got)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured=%T %#v", result.StructuredContent, result.StructuredContent)
	}
	if structured["requestId"] != "lreq_test" || structured["traceId"] != "trace_test" {
		t.Fatalf("structured=%v", structured)
	}
	output, ok := structured["result"].(map[string]any)
	if !ok || output["content"] != "ok" {
		t.Fatalf("result=%v", structured["result"])
	}
}

func TestLocalMCPRejectsInvalidTimeoutBeforeBridgeCall(t *testing.T) {
	t.Parallel()
	called := false
	server := newServer(t.TempDir(), "test", slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, string, protocolv1.CapabilityRequest) (protocolv1.CapabilityResponse, error) {
		called = true
		return protocolv1.CapabilityResponse{}, nil
	})
	client := connectTestClient(t, server)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "local_capability",
		Arguments: map[string]any{
			"capability":     "file.read",
			"action":         "read",
			"timeoutSeconds": 601,
		},
	})
	if err == nil && !result.IsError {
		t.Fatalf("invalid timeout succeeded: %#v", result)
	}
	if called {
		t.Fatal("bridge was called for invalid input")
	}
}

func connectTestClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "local-mcp-test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}
