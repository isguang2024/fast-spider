package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/isguang2024/fast-spider/internal/node"
)

type stopJobExecutor struct {
	mu         sync.Mutex
	cancelErr  error
	cancelSnap nativeRunnerJobSnapshot
	watch      nativeRunnerJobSnapshot
	checks     []string
	cancelled  []string
}

func (e *stopJobExecutor) Start(context.Context, nativeRunnerJobSpec) (nativeRunnerJobSnapshot, error) {
	return nativeRunnerJobSnapshot{}, errors.New("not used")
}

func (e *stopJobExecutor) Watch(context.Context, string) (nativeRunnerJobSnapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watch, nil
}

func (e *stopJobExecutor) CancelOwned(_ context.Context, jobID, key string) (nativeRunnerJobSnapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks = append(e.checks, jobID+"/"+key)
	if e.cancelErr != nil {
		return nativeRunnerJobSnapshot{}, e.cancelErr
	}
	e.cancelled = append(e.cancelled, jobID+"/"+key)
	return e.cancelSnap, nil
}

func TestNativeRunnerStopWithEmptyCloudBindingStillCancelsValidationJob(t *testing.T) {
	executor := &stopJobExecutor{cancelSnap: nativeRunnerJobSnapshot{JobID: "job-check", State: "running"}, watch: nativeRunnerJobSnapshot{JobID: "job-check", State: "running"}}
	transport := &nativeRunnerTransport{jobs: nativeRunnerJobStore{executor: executor}}
	task := nativeRunnerTask{ID: "task", Round: 2, Validations: map[string]nativeRunnerValidation{"unit": {JobID: "job-check"}}}
	result, err := transport.Stop(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stopped || len(result.WaitingJobs) != 1 || result.WaitingJobs[0] != "job-check" {
		t.Fatalf("running validation was released: %+v", result)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	wantKey := nativeRunnerCheckIdempotencyKey(task.ID, task.Round, "unit")
	if len(executor.cancelled) != 1 || executor.cancelled[0] != "job-check/"+wantKey {
		t.Fatalf("validation cancellation key=%v want=%s", executor.cancelled, wantKey)
	}
}

func TestNativeRunnerStopOwnerMismatchDoesNotCancel(t *testing.T) {
	executor := &stopJobExecutor{cancelErr: node.ErrNativeRunnerJobOwnership, watch: nativeRunnerJobSnapshot{JobID: "job-other", State: "running"}}
	transport := &nativeRunnerTransport{jobs: nativeRunnerJobStore{executor: executor}}
	task := nativeRunnerTask{ID: "task", Round: 1, Validations: map[string]nativeRunnerValidation{"unit": {JobID: "job-other"}}}
	result, err := transport.Stop(context.Background(), task)
	if err == nil || !errors.Is(err, node.ErrNativeRunnerJobOwnership) {
		t.Fatalf("owner mismatch err=%v", err)
	}
	if result.Stopped {
		t.Fatal("owner mismatch released task")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.checks) != 1 || len(executor.cancelled) != 0 {
		t.Fatalf("ownership mismatch reached cancellation: checks=%v cancels=%v", executor.checks, executor.cancelled)
	}
}

func TestNativeRunnerStopCompletedValidationJobAllowsStop(t *testing.T) {
	executor := &stopJobExecutor{cancelSnap: nativeRunnerJobSnapshot{JobID: "job-done", State: "completed", ExitCode: intPtr(0)}, watch: nativeRunnerJobSnapshot{JobID: "job-done", State: "completed", ExitCode: intPtr(0)}}
	transport := &nativeRunnerTransport{jobs: nativeRunnerJobStore{executor: executor}}
	task := nativeRunnerTask{ID: "task", Round: 1, Validations: map[string]nativeRunnerValidation{"unit": {JobID: "job-done"}}}
	result, err := transport.Stop(context.Background(), task)
	if err != nil || !result.Stopped || len(result.WaitingJobs) != 0 || result.Proof != nil {
		t.Fatalf("completed validation stop=%+v err=%v", result, err)
	}
}

func TestNativeRunnerStopFormalResultUsesResultProofWithoutCloudProbe(t *testing.T) {
	transport := &nativeRunnerTransport{}
	task := nativeRunnerTask{
		ID:      "task",
		Round:   3,
		Receipt: &nativeRunnerReceipt{SessionID: "chat", Generation: 3},
		Result:  &nativeRunnerResult{EventID: "event-formal", Outcome: "completed", Terminal: true},
	}
	result, err := transport.Stop(context.Background(), task)
	if err != nil || !result.Stopped || result.Proof == nil || !result.Proof.Terminal || result.Proof.SessionID != "chat" || result.Proof.Round != 3 {
		t.Fatalf("formal result stop=%+v err=%v", result, err)
	}
}

func TestNativeRunnerStopUnknownCloudHasNoInactiveProof(t *testing.T) {
	const session = "stop-unknown-cloud"
	var mu sync.Mutex
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/backend-api/sentinel/chat-requirements/prepare":
			writeChatGPTCloudTestJSON(t, w, map[string]any{"prepare_token": "test", "proofofwork": map[string]any{"required": true, "seed": "seed", "difficulty": "f"}})
		case chatgptStopPath:
			cancelled = true
			writeChatGPTCloudTestJSON(t, w, map[string]any{})
		case "/backend-api/conversation/" + session:
			writeChatGPTCloudTestJSON(t, w, nativeRecoveryCloudDetail(session, "assistant-1", "", ""))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	adapter.baseURL, adapter.http = server.URL, server.Client()
	adapter.cacheConduit(session, "conduit")
	manager := &AgentManager{chatgptCloud: adapter}
	transport := newNativeRunnerTransport(manager, t.TempDir())
	task := nativeRecoveryTask("project", "task", session, 1)
	result, err := transport.Stop(context.Background(), task)
	if err == nil || result.Stopped || result.Proof != nil {
		t.Fatalf("unknown Cloud state became proof: result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !cancelled {
		t.Fatal("Cloud cancellation was not requested")
	}
}

func TestNativeRunnerStopRepeatedCloudCancellationUsesFreshProof(t *testing.T) {
	const session = "stop-repeated-cloud"
	var mu sync.Mutex
	cancelCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/backend-api/sentinel/chat-requirements/prepare":
			writeChatGPTCloudTestJSON(t, w, map[string]any{"prepare_token": "test", "proofofwork": map[string]any{"required": true, "seed": "seed", "difficulty": "f"}})
		case chatgptStopPath:
			cancelCount++
			writeChatGPTCloudTestJSON(t, w, map[string]any{})
		case "/backend-api/conversation/" + session:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"conversation_id": session, "async_status": "canceled", "mapping": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	adapter.baseURL, adapter.http = server.URL, server.Client()
	adapter.cacheConduit(session, "conduit")
	transport := newNativeRunnerTransport(&AgentManager{chatgptCloud: adapter}, t.TempDir())
	task := nativeRecoveryTask("project", "task", session, 1)
	for i := 0; i < 2; i++ {
		result, err := transport.Stop(context.Background(), task)
		if err != nil || !result.Stopped || result.Proof == nil || !result.Proof.Terminal {
			t.Fatalf("repeat %d result=%+v err=%v", i, result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if cancelCount != 2 {
		t.Fatalf("expected idempotent repeated stop requests, count=%d", cancelCount)
	}
}
