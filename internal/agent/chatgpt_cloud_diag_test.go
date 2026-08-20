//go:build codexe2e

package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChatGPTCloudDiag(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CHATGPT_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CHATGPT_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	codex := NewCodexAdapter(nil)
	defer codex.Close(context.Background())
	_, _ = codex.Availability(ctx)
	token, err := codex.AuthToken(ctx)
	if err != nil {
		t.Fatalf("AuthToken: %v", err)
	}
	client := newChatGPTCloudHTTPClient()
	base := "https://chatgpt.com"

	check := func(name, method, url string, body string) {
		var req *http.Request
		if body != "" {
			req, _ = http.NewRequestWithContext(ctx, method, base+url, strings.NewReader(body))
		} else {
			req, _ = http.NewRequestWithContext(ctx, method, base+url, nil)
		}
		chatgptApplyCloudHeaders(req, token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("%s: ERR %v\n", name, err)
			return
		}
		defer resp.Body.Close()
		fmt.Printf("%s: %s\n", name, resp.Status)
	}
	check("GET /models", http.MethodGet, "/backend-api/models", "")
	check("GET /me", http.MethodGet, "/backend-api/me", "")
	key := chatgptRequirementsKey(time.Now())
	check("POST sentinel/prepare", http.MethodPost, "/backend-api/sentinel/chat-requirements/prepare", `{"p":`+strconvQuote(key)+`}`)
	check("GET /conversations", http.MethodGet, "/backend-api/conversations?limit=3", "")
}
