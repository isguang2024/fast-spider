package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGPTCloudRateLimitPreservedAcrossReadAndSendStages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "PRIVATE_PROVIDER_BODY")
	}))
	defer server.Close()
	a := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	defer a.Close(context.Background())
	a.baseURL, a.http = server.URL, server.Client()
	for name, call := range map[string]func() error{
		"read":    func() error { _, e := a.Read(context.Background(), "chat"); return e },
		"prepare": func() error { _, e := a.prepare(context.Background(), "token", map[string]any{}); return e },
		"stream":  func() error { _, e := a.stream(context.Background(), "token", "", map[string]any{}, nil); return e },
		"quick":   func() error { _, e := a.streamQuick(context.Background(), "token", map[string]any{}, nil); return e },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var ce interface{ CapabilityError() (string, string, bool) }
			if !errors.As(err, &ce) {
				t.Fatalf("HTTP classification lost: %v", err)
			}
			code, message, retryable := ce.CapabilityError()
			if code != "CHATGPT_CLOUD_RATE_LIMITED" || !retryable || !strings.Contains(message, "HTTP 429") || !strings.Contains(message, "120 seconds") || strings.Contains(err.Error(), "PRIVATE_PROVIDER_BODY") {
				t.Fatalf("unexpected error: %s %s %v", code, message, retryable)
			}
		})
	}
}

func TestChatGPTCloudHTTPErrorRetryAfterDoesNotEchoArbitraryHeaders(t *testing.T) {
	e := newChatGPTCloudHTTPError("read conversation", &http.Response{StatusCode: 403, Header: http.Header{"Retry-After": []string{"PRIVATE_HEADER"}}})
	code, message, retryable := e.CapabilityError()
	if code != "CHATGPT_CLOUD_FORBIDDEN" || retryable || strings.Contains(message, "PRIVATE_HEADER") {
		t.Fatalf("%s %s %v", code, message, retryable)
	}
}
