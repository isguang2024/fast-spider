package localmcp

import (
	"context"
	"testing"

	"github.com/isguang2024/fast-spider/hostapi"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type extensionMCPSurface struct {
	called bool
	result map[string]any
	err    error
}

func (s *extensionMCPSurface) RegisterMCP(_ *mcp.Server, ctx hostapi.MCPSurfaceContext) error {
	s.called = true
	s.result, s.err = ctx.Capabilities.CallCapability(context.Background(), "file.read", "read", map[string]any{"path": "example"})
	return nil
}

func TestMCPSurfaceReceivesExistingCapabilityTransport(t *testing.T) {
	surface := &extensionMCPSurface{}
	call := func(_ context.Context, dataDir string, request protocolv1.CapabilityRequest) (protocolv1.CapabilityResponse, error) {
		if dataDir != "test-data" {
			t.Fatalf("dataDir=%q", dataDir)
		}
		if request.Capability != "file.read" || request.Action != "read" {
			t.Fatalf("request=%+v", request)
		}
		return protocolv1.CapabilityResponse{Result: map[string]any{"ok": true}}, nil
	}
	server, err := newServerWithSurfaces("test-data", "test", nil, call, []hostapi.MCPSurface{surface})
	if err != nil {
		t.Fatal(err)
	}
	if server == nil || !surface.called {
		t.Fatal("expected MCP surface registration")
	}
	if surface.err != nil || surface.result["ok"] != true {
		t.Fatalf("surface capability result=%v err=%v", surface.result, surface.err)
	}
}
