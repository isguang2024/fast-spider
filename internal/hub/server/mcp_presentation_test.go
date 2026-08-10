package server

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPresentationToolResultCreatesImageAndExternalResourceLink(t *testing.T) {
	imageData := []byte("png-image-data")
	result := presentationToolResultWithFetcher(context.Background(), map[string]any{
		"publicUrl":   "https://assets.salesmartly.com/shot.png",
		"fileName":    "shot.png",
		"contentType": "image/png",
		"sizeBytes":   int64(1234),
	}, func(_ context.Context, publicURL, mimeType string) ([]byte, error) {
		if publicURL != "https://assets.salesmartly.com/shot.png" || mimeType != "image/png" {
			t.Fatalf("fetch arguments url=%q mime=%q", publicURL, mimeType)
		}
		return imageData, nil
	})
	if result == nil || len(result.Content) != 2 {
		t.Fatalf("presentation result=%+v", result)
	}
	image, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("first content type=%T want *mcp.ImageContent", result.Content[0])
	}
	if image.MIMEType != "image/png" || !bytes.Equal(image.Data, imageData) {
		t.Fatalf("image content=%+v", image)
	}
	link, ok := result.Content[1].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("second content type=%T want *mcp.ResourceLink", result.Content[1])
	}
	if link.URI != "https://assets.salesmartly.com/shot.png" || link.Name != "shot.png" || link.MIMEType != "image/png" {
		t.Fatalf("resource link=%+v", link)
	}
	if link.Size == nil || *link.Size != 1234 {
		t.Fatalf("resource link size=%v", link.Size)
	}
}

func TestPresentationToolResultKeepsLinkWhenImageFetchFails(t *testing.T) {
	result := presentationToolResultWithFetcher(context.Background(), map[string]any{
		"publicUrl":   "https://assets.salesmartly.com/shot.jpg",
		"fileName":    "shot.jpg",
		"contentType": "image/jpeg",
	}, func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("temporary fetch failure")
	})
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ResourceLink); !ok {
		t.Fatalf("content type=%T want *mcp.ResourceLink", result.Content[0])
	}
}

func TestPresentationToolResultDoesNotFetchNonImageFile(t *testing.T) {
	called := false
	result := presentationToolResultWithFetcher(context.Background(), map[string]any{
		"publicUrl":   "https://assets.salesmartly.com/archive.zip",
		"fileName":    "archive.zip",
		"contentType": "application/zip",
	}, func(context.Context, string, string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if called {
		t.Fatal("non-image presentation unexpectedly fetched content")
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ResourceLink); !ok {
		t.Fatalf("content type=%T want *mcp.ResourceLink", result.Content[0])
	}
}

func TestFetchPresentationImageRejectsNonSaleSmartlyURL(t *testing.T) {
	if _, err := fetchPresentationImage(context.Background(), "https://example.test/shot.png", "image/png"); err == nil {
		t.Fatal("non-SaleSmartly image URL was accepted")
	}
}

func TestFetchPresentationImageRealSaleSmartly(t *testing.T) {
	publicURL := strings.TrimSpace(os.Getenv("FAST_SPIDER_PRESENTATION_IMAGE_E2E_URL"))
	if publicURL == "" {
		t.Skip("set FAST_SPIDER_PRESENTATION_IMAGE_E2E_URL to validate SaleSmartly image embedding")
	}
	data, err := fetchPresentationImage(context.Background(), publicURL, "image/png")
	if err != nil {
		t.Fatalf("fetch real SaleSmartly presentation image: %v", err)
	}
	if len(data) == 0 || int64(len(data)) > maxMCPPresentationImageBytes {
		t.Fatalf("real presentation image size=%d", len(data))
	}
}

func TestPresentationToolResultIgnoresOrdinaryCapabilityResults(t *testing.T) {
	if result := presentationToolResult(context.Background(), map[string]any{"status": "ok"}); result != nil {
		t.Fatalf("unexpected presentation result=%+v", result)
	}
}
