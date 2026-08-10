package node

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaleSmartlyPublisherRealSTS(t *testing.T) {
	if os.Getenv("FAST_SPIDER_SALESMARTLY_E2E") != "1" {
		t.Skip("set FAST_SPIDER_SALESMARTLY_E2E=1 to validate the real SaleSmartly STS flow")
	}
	publisher := newSaleSmartlyPublisher()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, pluginID := range saleSmartlyPluginCandidates {
		lease, err := publisher.acquireLease(ctx, pluginID, "2")
		if err != nil {
			t.Fatalf("acquire real SaleSmartly STS for %s: %v", pluginID, err)
		}
		if lease.Path == "" || lease.AccessKeyID == "" || lease.AccessKeySecret == "" || lease.SecurityToken == "" {
			t.Fatalf("real SaleSmartly STS response is incomplete for %s", pluginID)
		}
	}
}

func TestSaleSmartlyPublisherRealUpload(t *testing.T) {
	if os.Getenv("FAST_SPIDER_SALESMARTLY_UPLOAD_E2E") != "1" {
		t.Skip("set FAST_SPIDER_SALESMARTLY_UPLOAD_E2E=1 to validate a real SaleSmartly OSS upload")
	}
	publisher := newSaleSmartlyPublisher()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	filePath := filepath.Join(t.TempDir(), "fast-spider-e2e.png")
	if err := os.WriteFile(filePath, []byte("fast-spider presentation e2e"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := publisher.PublishFile(ctx, filePath, "fast-spider-e2e.png", "image/png")
	if err != nil {
		t.Fatalf("publish real SaleSmartly object: %v", err)
	}
	if result.URL == "" {
		t.Fatal("real SaleSmartly upload returned no public URL")
	}

	lastStatus := 0
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, result.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := publisher.http.Do(req)
		if err == nil {
			lastStatus = resp.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("published SaleSmartly URL was not readable, last status=%d", lastStatus)
}
