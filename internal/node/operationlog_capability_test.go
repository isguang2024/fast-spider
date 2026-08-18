package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/operationlog"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestOperationLogCapabilityQueriesBoundedSanitizedPages(t *testing.T) {
	store, err := operationlog.NewStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := operationlog.NewEntry(operationlog.LevelInfo, "http", "request", "secret message")
	first.Timestamp = time.Now().Add(-2 * time.Second).UnixMilli()
	first.Path = "/users/alice/private"
	first.ClientIP = "10.0.0.1"
	store.Append(first)
	second := operationlog.NewEntry(operationlog.LevelWarning, "capability", "file.read", "another secret")
	second.Timestamp = first.Timestamp - 1
	store.Append(second)

	client, err := New(Config{DataDir: t.TempDir(), Version: "operation-log-test", OperationLog: store})
	if err != nil {
		t.Fatal(err)
	}
	call := func(params map[string]any) protocolv1.CapabilityResponse {
		return client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_operation_log_test",
			Capability:  "operation.log",
			Action:      "query",
			Params:      params,
		})
	}

	page := call(map[string]any{"limit": 1})
	if page.Error != nil {
		t.Fatalf("operation log query error=%+v", page.Error)
	}
	if page.Result["hasMore"] != true || page.Result["nextCursor"] == "" {
		t.Fatalf("page did not expose cursor: %+v", page.Result)
	}
	raw, _ := json.Marshal(page.Result)
	for _, forbidden := range []string{"secret message", "another secret", "/users/alice/private", "10.0.0.1", "clientIp", "path", "message", "extra"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("operation log result leaked %q: %s", forbidden, raw)
		}
	}

	next := call(map[string]any{"limit": 10, "before": page.Result["nextCursor"]})
	if next.Error != nil || len(next.Result["entries"].([]any)) == 0 {
		t.Fatalf("cursor page=%+v", next)
	}
	invalid := call(map[string]any{"limit": 201})
	if invalid.Error == nil || invalid.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("invalid limit response=%+v", invalid)
	}
}

func TestOperationLogCapabilityUnavailableIsExplicit(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "operation-log-unavailable-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   "req_operation_log_unavailable",
		Capability:  "operation.log",
		Action:      "query",
		Params:      map[string]any{},
	})
	if response.Error == nil || response.Error.Code != "OPERATION_LOG_UNAVAILABLE" {
		t.Fatalf("unavailable response=%+v", response)
	}
}
