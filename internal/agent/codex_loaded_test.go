package agent

import (
	"context"
	"errors"
	"strings"
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

func TestCodexArchiveLifecycleLoadsThreadBeforeChangingArchiveState(t *testing.T) {
	for _, test := range []struct {
		name       string
		archive    bool
		method     string
		wantCalls  string
		wantLoaded bool
	}{
		{name: "archive", archive: true, method: "thread/archive", wantCalls: "thread/resume,thread/archive,thread/unsubscribe"},
		{name: "unarchive", archive: false, method: "thread/unarchive", wantCalls: "thread/unarchive,thread/resume", wantLoaded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := NewCodexAdapter(nil)
			var methods []string
			adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				if params["threadId"] != "thread-1" {
					t.Fatalf("unexpected params=%#v", params)
				}
				methods = append(methods, method)
				switch method {
				case "thread/resume", "thread/unsubscribe", test.method:
					return map[string]any{}, nil
				default:
					return nil, errors.New("unexpected method " + method)
				}
			}

			var err error
			if test.archive {
				err = adapter.ArchiveThread(context.Background(), "thread-1")
			} else {
				err = adapter.UnarchiveThread(context.Background(), "thread-1")
			}
			if err != nil {
				t.Fatalf("archive lifecycle error: %v", err)
			}
			if got, want := strings.Join(methods, ","), test.wantCalls; got != want {
				t.Fatalf("methods=%q want=%q", got, want)
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

func TestCodexUnloadThreadIsIdempotentAndDoesNotReleaseActiveTurn(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-idle"] = struct{}{}
	adapter.loaded["thread-active"] = struct{}{}
	adapter.activeTurns["thread-active"] = "turn-1"
	var calls []string
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		calls = append(calls, method+":"+params["threadId"].(string))
		return map[string]any{}, nil
	}

	if err := adapter.unloadThread(context.Background(), "thread-idle"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.unloadThread(context.Background(), "thread-idle"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.unloadThread(context.Background(), "thread-active"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(calls, ","), "thread/unsubscribe:thread-idle"; got != want {
		t.Fatalf("calls=%q want=%q", got, want)
	}
	adapter.mu.Lock()
	_, idleLoaded := adapter.loaded["thread-idle"]
	_, activeLoaded := adapter.loaded["thread-active"]
	adapter.mu.Unlock()
	if idleLoaded || !activeLoaded {
		t.Fatalf("loaded idle=%v active=%v", idleLoaded, activeLoaded)
	}
}

func TestCodexUnloadThreadClearsAlreadyUnsubscribedState(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-1"] = struct{}{}
	adapter.requestOverride = func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, newExecutionError("codex", "thread/unsubscribe", "thread is not subscribed")
	}
	if err := adapter.unloadThread(context.Background(), "thread-1"); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	_, loaded := adapter.loaded["thread-1"]
	adapter.mu.Unlock()
	if loaded {
		t.Fatal("already-unsubscribed thread remained loaded")
	}
}

func TestCodexCompletedTurnUnloadsThread(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-1"] = struct{}{}
	adapter.activeTurns["thread-1"] = "turn-1"
	unsubscribed := make(chan struct{})
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if method != "thread/unsubscribe" || params["threadId"] != "thread-1" {
			t.Fatalf("unexpected request method=%q params=%#v", method, params)
		}
		close(unsubscribed)
		return map[string]any{}, nil
	}

	adapter.handleNotification("turn/completed", []byte(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	select {
	case <-unsubscribed:
	case <-time.After(time.Second):
		t.Fatal("completed turn did not unsubscribe its thread")
	}
	deadline := time.Now().Add(time.Second)
	loaded := true
	for loaded && time.Now().Before(deadline) {
		adapter.mu.Lock()
		_, loaded = adapter.loaded["thread-1"]
		adapter.mu.Unlock()
		if loaded {
			time.Sleep(time.Millisecond)
		}
	}
	if loaded || adapter.ActiveTurn("thread-1") != "" {
		t.Fatalf("completed thread loaded=%v active=%q", loaded, adapter.ActiveTurn("thread-1"))
	}
}

func TestCodexUnloadDoesNotCrossInFlightTurnStart(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.loaded["thread-1"] = struct{}{}
	turnStartEntered := make(chan struct{})
	releaseTurnStart := make(chan struct{})
	unsubscribeRequested := make(chan struct{}, 1)
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		switch method {
		case "turn/start":
			close(turnStartEntered)
			<-releaseTurnStart
			return map[string]any{"turn": map[string]any{"id": "turn-1"}}, nil
		case "thread/unsubscribe":
			unsubscribeRequested <- struct{}{}
			return map[string]any{}, nil
		default:
			return nil, errors.New("unexpected method " + method)
		}
	}

	startDone := make(chan error, 1)
	go func() {
		_, err := adapter.StartTurn(context.Background(), "thread-1", "continue", "", "", "")
		startDone <- err
	}()
	select {
	case <-turnStartEntered:
	case <-time.After(time.Second):
		t.Fatal("turn/start did not enter the controlled request")
	}
	unloadDone := make(chan error, 1)
	go func() { unloadDone <- adapter.unloadThread(context.Background(), "thread-1") }()
	select {
	case <-unsubscribeRequested:
		t.Fatal("thread was unsubscribed while turn/start was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseTurnStart)
	if err := <-startDone; err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := <-unloadDone; err != nil {
		t.Fatalf("unload thread: %v", err)
	}
	select {
	case <-unsubscribeRequested:
		t.Fatal("active thread was unsubscribed after turn/start completed")
	default:
	}
	if got := adapter.ActiveTurn("thread-1"); got != "turn-1" {
		t.Fatalf("active turn=%q want turn-1", got)
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
