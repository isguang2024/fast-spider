package node

import (
	"context"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type activityTestAgent struct{ busy bool }

func (a *activityTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (a *activityTestAgent) Close(context.Context) error { return nil }
func (a *activityTestAgent) BusyForUpdate() bool         { return a.busy }

func TestTaskBusyReasonsAndRuntimeStatus(t *testing.T) {
	client := NewLocalCapabilityClient(Config{DataDir: t.TempDir(), Agent: &activityTestAgent{busy: true}})
	client.jobs.mu.Lock()
	client.jobs.jobs["job"] = &Job{state: "running"}
	client.jobs.mu.Unlock()
	client.browser.mu.Lock()
	client.browser.session = &browserSessionRecord{BrowserSessionID: "brs_test"}
	client.browser.mu.Unlock()
	client.requestSem <- struct{}{}

	reasons := client.TaskBusyReasons()
	if len(reasons) != 4 || client.RuntimeStatus() != "busy" {
		t.Fatalf("reasons=%v status=%s", reasons, client.RuntimeStatus())
	}
	<-client.requestSem
	client.jobs.mu.Lock()
	client.jobs.jobs["job"].state = "completed"
	client.jobs.mu.Unlock()
	client.browser.mu.Lock()
	client.browser.session = nil
	client.browser.mu.Unlock()
	client.agent = &activityTestAgent{}
	if reasons := client.TaskBusyReasons(); len(reasons) != 0 || client.RuntimeStatus() != "ready" {
		t.Fatalf("idle reasons=%v status=%s", reasons, client.RuntimeStatus())
	}
}

func TestReleaseDrainRejectsNewCapability(t *testing.T) {
	client := NewLocalCapabilityClient(Config{DataDir: t.TempDir()})
	if !client.BeginReleaseDrain() || !client.ReleaseDraining() || client.RuntimeStatus() != "busy" {
		t.Fatal("client did not enter release drain")
	}
	response := client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   "req-test",
		Capability:  "file.read",
		Action:      "read",
	})
	if response.Error == nil || response.Error.Code != "NODE_UPDATING" || !response.Error.Retryable {
		t.Fatalf("response=%+v", response)
	}
	client.EndReleaseDrain()
	if client.ReleaseDraining() {
		t.Fatal("release drain remained active")
	}
}
