package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/core"
)

func TestWriteDirectErrorPreservesSessionCreateRecoveryContract(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeDirectError(recorder, &core.CapabilityCallError{
		Code:      "DEADLINE_EXCEEDED",
		Message:   "retry the same request with the original idempotencyKey",
		Retryable: true,
		Details: map[string]any{
			"mayHaveCreated": true,
			"recovery":       "retry_same_idempotency_key",
		},
	})
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want 504", recorder.Code)
	}
	var payload apiError
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "DEADLINE_EXCEEDED" || !payload.Error.Retryable {
		t.Fatalf("protocol error=%+v", payload.Error)
	}
	if payload.Error.Details["recovery"] != "retry_same_idempotency_key" || payload.Error.Details["mayHaveCreated"] != true {
		t.Fatalf("recovery details=%#v", payload.Error.Details)
	}
}
