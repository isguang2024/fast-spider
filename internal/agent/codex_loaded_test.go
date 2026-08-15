package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func TestCodexDeleteThreadClearsLoadedState(t *testing.T) {
	tests := []struct {
		name       string
		requestErr error
		wantLoaded bool
	}{
		{name: "deleted"},
		{name: "already missing", requestErr: node.ErrAgentSessionNotFound},
		{name: "transient failure", requestErr: errors.New("temporary RPC failure"), wantLoaded: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := NewCodexAdapter(nil)
			adapter.loaded["thread-1"] = struct{}{}
			adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				if method != "thread/delete" || params["threadId"] != "thread-1" {
					t.Fatalf("unexpected request method=%q params=%#v", method, params)
				}
				return map[string]any{}, test.requestErr
			}

			err := adapter.DeleteThread(context.Background(), "thread-1")
			if !errors.Is(err, test.requestErr) {
				t.Fatalf("delete error=%v want=%v", err, test.requestErr)
			}
			adapter.mu.Lock()
			_, loaded := adapter.loaded["thread-1"]
			adapter.mu.Unlock()
			if loaded != test.wantLoaded {
				t.Fatalf("loaded=%v want=%v", loaded, test.wantLoaded)
			}
		})
	}
}

func TestCodexDeleteSerializesWithThreadResume(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	resumeStarted := make(chan struct{})
	releaseResume := make(chan struct{})
	deleteRequested := make(chan struct{})
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "thread-race" {
			t.Fatalf("unexpected params=%#v", params)
		}
		switch method {
		case "thread/resume":
			close(resumeStarted)
			<-releaseResume
			return map[string]any{}, nil
		case "thread/delete":
			close(deleteRequested)
			return map[string]any{}, nil
		default:
			return nil, errors.New("unexpected method " + method)
		}
	}
	resumeDone := make(chan error, 1)
	go func() { resumeDone <- adapter.ensureThreadLoaded(context.Background(), "thread-race") }()
	select {
	case <-resumeStarted:
	case <-time.After(time.Second):
		t.Fatal("thread resume did not start")
	}
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- adapter.DeleteThread(context.Background(), "thread-race") }()
	select {
	case <-deleteRequested:
		t.Fatal("thread delete raced ahead of the in-flight resume")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseResume)
	if err := <-resumeDone; err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	adapter.mu.Lock()
	_, loaded := adapter.loaded["thread-race"]
	adapter.mu.Unlock()
	if loaded {
		t.Fatal("completed delete left the concurrently resumed thread marked loaded")
	}
}
