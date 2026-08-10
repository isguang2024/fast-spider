package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaleSmartlyPublisherUsesThreeInternalCandidates(t *testing.T) {
	want := []string{"b87x3n", "g101rfh", "f11lv7v"}
	if len(saleSmartlyPluginCandidates) != len(want) {
		t.Fatalf("plugin candidates=%v want=%v", saleSmartlyPluginCandidates, want)
	}
	for index := range want {
		if saleSmartlyPluginCandidates[index] != want[index] {
			t.Fatalf("plugin candidates=%v want=%v", saleSmartlyPluginCandidates, want)
		}
	}
}

func TestSaleSmartlyPublisherCachesSTSForTwentyMinutes(t *testing.T) {
	var mu sync.Mutex
	guestCalls := 0
	stsCalls := 0
	ossCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/msg-user/create-user":
			mu.Lock()
			guestCalls++
			mu.Unlock()
			writeSaleSmartlyTestEnvelope(t, w, map[string]any{"token": "guest-token"})
		case "/sys/company/plugin/get-oss-config":
			mu.Lock()
			stsCalls++
			mu.Unlock()
			writeSaleSmartlyTestEnvelope(t, w, map[string]any{
				"path": "tenant/presentation",
				"dews": 3,
				"sts_config": map[string]any{
					"access_key_id":     "test-ak",
					"access_key_secret": "test-secret",
					"security_token":    "test-token",
					"expiration":        "2100-01-01T00:00:00Z",
				},
			})
		case "/oss":
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			ossCalls++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	current := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
	publisher := newSaleSmartlyPublisher()
	publisher.http = server.Client()
	publisher.apiBase = server.URL
	publisher.ossOrigin = server.URL + "/oss"
	publisher.assetOrigin = "https://assets.example.test"
	publisher.now = func() time.Time { return current }

	filePath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(filePath, []byte("fake-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		result, err := publisher.PublishFile(context.Background(), filePath, "shot.png", "image/png")
		if err != nil {
			t.Fatalf("publish %d: %v", index, err)
		}
		if !strings.HasPrefix(result.URL, "https://assets.example.test/tenant/presentation/fast-spider/20260810/") {
			t.Fatalf("unexpected public URL: %s", result.URL)
		}
	}
	mu.Lock()
	if guestCalls != 1 || stsCalls != 1 || ossCalls != 2 {
		t.Fatalf("before expiry guest=%d sts=%d oss=%d", guestCalls, stsCalls, ossCalls)
	}
	mu.Unlock()

	current = current.Add(20*time.Minute + time.Second)
	if _, err := publisher.PublishFile(context.Background(), filePath, "shot.png", "image/png"); err != nil {
		t.Fatalf("publish after expiry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if guestCalls != 2 || stsCalls != 2 || ossCalls != 3 {
		t.Fatalf("after expiry guest=%d sts=%d oss=%d", guestCalls, stsCalls, ossCalls)
	}
}

func TestSaleSmartlyPublisherFailsOverToSecondPlugin(t *testing.T) {
	var mu sync.Mutex
	seen := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.URL.Query().Get("plugin_id")
		switch r.URL.Path {
		case "/chat/msg-user/create-user":
			writeSaleSmartlyTestEnvelope(t, w, map[string]any{"token": "guest-" + pluginID})
		case "/sys/company/plugin/get-oss-config":
			mu.Lock()
			seen = append(seen, pluginID)
			mu.Unlock()
			if pluginID == saleSmartlyPluginCandidates[0] {
				_, _ = fmt.Fprint(w, `{"code":1,"message":"candidate unavailable","data":{}}`)
				return
			}
			writeSaleSmartlyTestEnvelope(t, w, map[string]any{
				"path": "tenant/presentation",
				"dews": 3,
				"sts_config": map[string]any{
					"access_key_id":     "test-ak",
					"access_key_secret": "test-secret",
					"security_token":    "test-token",
					"expiration":        "2100-01-01T00:00:00Z",
				},
			})
		case "/oss":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	publisher := newSaleSmartlyPublisher()
	publisher.http = server.Client()
	publisher.apiBase = server.URL
	publisher.ossOrigin = server.URL + "/oss"
	publisher.assetOrigin = "https://assets.example.test"
	publisher.now = func() time.Time { return time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC) }
	filePath := filepath.Join(t.TempDir(), "bundle.zip")
	if err := os.WriteFile(filePath, []byte("zip-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishFile(context.Background(), filePath, "bundle.zip", "application/zip"); err != nil {
		t.Fatalf("publish with failover: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != saleSmartlyPluginCandidates[0] || seen[1] != saleSmartlyPluginCandidates[1] {
		t.Fatalf("plugin failover order=%v", seen)
	}
}

func writeSaleSmartlyTestEnvelope(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "ok", "data": data}); err != nil {
		t.Fatal(err)
	}
}
