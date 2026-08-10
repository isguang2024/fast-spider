package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPresentationToolResultCreatesExternalResourceLink(t *testing.T) {
	result := presentationToolResult(map[string]any{
		"publicUrl":   "https://example.test/shot.png",
		"fileName":    "shot.png",
		"contentType": "image/png",
		"sizeBytes":   int64(1234),
	})
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	link, ok := result.Content[0].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("content type=%T want *mcp.ResourceLink", result.Content[0])
	}
	if link.URI != "https://example.test/shot.png" || link.Name != "shot.png" || link.MIMEType != "image/png" {
		t.Fatalf("resource link=%+v", link)
	}
	if link.Size == nil || *link.Size != 1234 {
		t.Fatalf("resource link size=%v", link.Size)
	}
}

func TestPresentationToolResultIgnoresOrdinaryCapabilityResults(t *testing.T) {
	if result := presentationToolResult(map[string]any{"status": "ok"}); result != nil {
		t.Fatalf("unexpected presentation result=%+v", result)
	}
}
