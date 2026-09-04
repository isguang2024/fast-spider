package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func TestCodexExecutableCandidatesHonorExplicitOverride(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "codex-test")
	t.Setenv("FAST_SPIDER_CODEX_EXECUTABLE", explicit)
	candidates := codexExecutableCandidates()
	if len(candidates) != 1 || !sameAgentPath(candidates[0], explicit) {
		t.Fatalf("explicit candidates=%#v", candidates)
	}
}

func TestCodexExecutableCandidatesPreferNewestDesktopRuntime(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Codex Desktop runtime discovery is Windows-specific")
	}
	base := t.TempDir()
	t.Setenv("FAST_SPIDER_CODEX_EXECUTABLE", "")
	t.Setenv("LOCALAPPDATA", base)
	older := filepath.Join(base, "OpenAI", "Codex", "bin", "old", "codex.exe")
	newer := filepath.Join(base, "OpenAI", "Codex", "bin", "new", "codex.exe")
	for _, path := range []string{older, newer} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(older, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}
	candidates := codexExecutableCandidates()
	if len(candidates) == 0 || !sameAgentPath(candidates[0], newer) {
		t.Fatalf("desktop candidates=%#v", candidates)
	}
}

func TestCodexExecutableCandidatesIgnoreMissingOrRelativeLocalAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Codex Desktop runtime discovery is Windows-specific")
	}
	base := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	fake := filepath.Join(base, "OpenAI", "Codex", "bin", "fake", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "relative"} {
		t.Setenv("FAST_SPIDER_CODEX_EXECUTABLE", "")
		t.Setenv("LOCALAPPDATA", value)
		for _, candidate := range codexExecutableCandidates() {
			if sameAgentPath(candidate, fake) {
				t.Fatalf("LOCALAPPDATA=%q selected relative Desktop candidate %q", value, candidate)
			}
		}
	}
}

func TestCodexAppServerCommandAlwaysUsesStdio(t *testing.T) {
	if got := codexAppServerCommandArgs(); !reflect.DeepEqual(got, []string{"app-server", "--stdio"}) {
		t.Fatalf("app-server args=%#v", got)
	}
}

func TestCodexAppServerEnvironmentPreservesProxyExceptionsAndAddsLoopback(t *testing.T) {
	got := codexAppServerEnvironment([]string{
		"PATH=test-path",
		"NO_PROXY=example.com, 127.0.0.1",
		"no_proxy=internal.test,EXAMPLE.com",
		"OTHER=value",
	})
	want := []string{
		"PATH=test-path",
		"OTHER=value",
		"NO_PROXY=example.com,127.0.0.1,internal.test,localhost",
		"no_proxy=example.com,127.0.0.1,internal.test,localhost",
		"LOG_FORMAT=json",
		"RUST_LOG=warn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managed app-server environment=%#v, want %#v", got, want)
	}
}

func TestCodexExecutionMetadataIsNodeOwnedAppServer(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	mode, owner := adapter.executionMetadata()
	if mode != "codex_app_server" || owner != "fast_spider_node" {
		t.Fatalf("execution metadata=(%q, %q)", mode, owner)
	}
}

func TestCodexSessionLoadLocksSerializePerSessionOnly(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	unlockA := adapter.lockSessionLoad("session-a")
	sameAcquired := make(chan struct{})
	otherAcquired := make(chan struct{})
	done := make(chan struct{})

	go func() {
		unlock := adapter.lockSessionLoad("session-a")
		close(sameAcquired)
		unlock()
		close(done)
	}()
	go func() {
		unlock := adapter.lockSessionLoad("session-b")
		close(otherAcquired)
		unlock()
	}()

	select {
	case <-otherAcquired:
	case <-time.After(time.Second):
		t.Fatal("different session was blocked by session-a")
	}
	select {
	case <-sameAcquired:
		t.Fatal("same session was not serialized")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same session did not resume after unlock")
	}
	adapter.loadLocksMu.Lock()
	remaining := len(adapter.loadLocks)
	adapter.loadLocksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("session lock entries leaked: %d", remaining)
	}
}

func TestCodexConfigWarningBecomesSanitizedCapabilityError(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.handleNotification("configWarning", json.RawMessage(`{"message":"Invalid configuration; using defaults","details":"config.toml token=secret-value"}`))
	adapter.mu.Lock()
	err := adapter.configErr
	adapter.mu.Unlock()
	if err == nil || err.Class != ErrorConfigInvalid {
		t.Fatalf("config warning error=%#v", err)
	}
	code, message, retryable := err.CapabilityError()
	if code != "AGENT_CONFIG_INVALID" || retryable || message != "AI runtime configuration is incompatible" {
		t.Fatalf("config warning capability error=(%q, %q, %v)", code, message, retryable)
	}
	if strings.Contains(err.debugText, "secret-value") || strings.Contains(err.debugText, "config.toml") {
		t.Fatalf("config warning retained raw details: %q", err.debugText)
	}
}

func TestCodexWaitLoopClearsActiveTurnsForExitedProcess(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_WAIT_HELPER") == "1" {
		os.Exit(1)
	}
	adapter := NewCodexAdapter(nil)
	cmd := exec.Command(os.Args[0], "-test.run=TestCodexWaitLoopClearsActiveTurnsForExitedProcess")
	cmd.Env = append(os.Environ(), "FAST_SPIDER_CODEX_WAIT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	adapter.cmd = cmd
	adapter.generation = 1
	done := make(chan struct{})
	adapter.processDone = done
	adapter.mu.Unlock()
	adapter.eventMu.Lock()
	adapter.activeTurns["session-1"] = "turn-1"
	adapter.eventMu.Unlock()
	adapter.waitLoop(cmd, 1, done)
	if active := adapter.ActiveTurn("session-1"); active != "" {
		t.Fatalf("active turn survived app-server exit: %q", active)
	}
	events, _, _, err := adapter.Watch(context.Background(), "session-1", 0, 0)
	if err != nil || len(events) != 1 || events[0].Type != "turn.failed" || events[0].TurnID != "turn-1" {
		t.Fatalf("process exit events=%#v err=%v", events, err)
	}
}

func TestCodexRequestTimeoutQuarantinesGeneration(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	cmd := &exec.Cmd{Process: &os.Process{Pid: 9001}}
	done := make(chan struct{})
	writer := &bufferWriteCloser{}
	adapter.mu.Lock()
	adapter.cmd = cmd
	adapter.stdin = writer
	adapter.processDone = done
	adapter.generation = 1
	adapter.mu.Unlock()
	adapter.stopProcessOverride = func(_ context.Context, got *exec.Cmd) error {
		if got != cmd {
			t.Fatalf("quarantine stopped a different process: %p != %p", got, cmd)
		}
		adapter.finishProcess(cmd, 1, done, errors.New("injected app-server timeout"))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := adapter.request(ctx, "getAuthStatus", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out RPC error=%v", err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.cmd != nil || adapter.stdin != nil || adapter.processDone != nil || adapter.quarantined {
		t.Fatalf("timed out generation remained reusable: cmd=%p stdin=%p done=%p quarantined=%v", adapter.cmd, adapter.stdin, adapter.processDone, adapter.quarantined)
	}
	if len(adapter.pending) != 0 {
		t.Fatalf("timed out RPC remained pending: %#v", adapter.pending)
	}
}

func TestCodexEnsureStartedBoundsQuarantinedGenerationWait(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.mu.Lock()
	adapter.cmd = &exec.Cmd{Process: &os.Process{Pid: 9002}}
	adapter.processDone = make(chan struct{})
	adapter.generation = 1
	adapter.quarantined = true
	adapter.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := adapter.ensureStarted(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quarantined generation error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("quarantined generation wait was unbounded: %s", elapsed)
	}
}

func TestCodexEnsureStartedHonorsContextWhileWaitingForStartGate(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	release, err := adapter.acquireStartGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := adapter.ensureStarted(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start gate wait error=%v", err)
	}
}

func TestCodexCloseSynchronizesWithStartPublish(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_CLOSE_HELPER") == "1" {
		select {}
	}
	adapter := NewCodexAdapter(nil)
	cmd := exec.Command(os.Args[0], "-test.run=TestCodexCloseSynchronizesWithStartPublish")
	cmd.Env = append(os.Environ(), "FAST_SPIDER_CODEX_CLOSE_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go adapter.waitLoop(cmd, 1, done)
	adapter.lifecycleMu.Lock()
	publishDone := make(chan bool, 1)
	go func() {
		_, published := adapter.publishStartedProcess(cmd, stdin, done)
		publishDone <- published
	}()
	closeDone := make(chan error, 1)
	go func() { closeDone <- adapter.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before start publish barrier: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	adapter.lifecycleMu.Unlock()
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("start publish did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after start publish")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("started child process remained active after Close")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.cmd != nil || adapter.processDone != nil || !adapter.closed {
		t.Fatalf("Close left adapter state: cmd=%p done=%p closed=%v", adapter.cmd, adapter.processDone, adapter.closed)
	}
}

func TestCodexQuarantineStopStaysBoundToOldGeneration(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	oldCmd := &exec.Cmd{Process: &os.Process{Pid: 9101}}
	oldDone := make(chan struct{})
	oldStdin := &trackingWriteCloser{written: make(chan struct{})}
	newCmd := &exec.Cmd{Process: &os.Process{Pid: 9102}}
	newDone := make(chan struct{})
	newStdin := &trackingWriteCloser{written: make(chan struct{})}
	stopEntered := make(chan codexProcess, 1)
	releaseStop := make(chan struct{})
	adapter.mu.Lock()
	adapter.cmd = oldCmd
	adapter.stdin = oldStdin
	adapter.processDone = oldDone
	adapter.generation = 1
	adapter.mu.Unlock()
	adapter.stopTargetOverride = func(_ context.Context, target codexProcess) error {
		stopEntered <- target
		<-releaseStop
		_ = target.stdin.Close()
		return nil
	}
	quarantineDone := make(chan struct{})
	go func() {
		adapter.quarantineProcess(1)
		close(quarantineDone)
	}()
	var target codexProcess
	select {
	case target = <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("old generation stop did not start")
	}
	if target.cmd != oldCmd || target.stdin != oldStdin || target.done != oldDone || target.generation != 1 {
		t.Fatalf("stop target=%#v", target)
	}

	adapter.mu.Lock()
	adapter.cmd = newCmd
	adapter.stdin = newStdin
	adapter.processDone = newDone
	adapter.generation = 2
	adapter.quarantined = false
	adapter.mu.Unlock()
	adapter.finishProcess(oldCmd, 1, oldDone, errors.New("old generation exited"))
	close(releaseStop)
	select {
	case <-quarantineDone:
	case <-time.After(time.Second):
		t.Fatal("old generation stop did not finish")
	}
	if got := atomic.LoadInt32(&oldStdin.closes); got != 1 {
		t.Fatalf("old stdin closes=%d want 1", got)
	}
	if got := atomic.LoadInt32(&newStdin.closes); got != 0 {
		t.Fatalf("replacement stdin was closed by old stop: %d", got)
	}
	select {
	case <-newDone:
		t.Fatal("old stop closed replacement processDone")
	default:
	}

	requestDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := adapter.request(ctx, "getAuthStatus", nil)
		requestDone <- err
	}()
	select {
	case <-newStdin.written:
	case <-time.After(time.Second):
		t.Fatal("replacement request did not write")
	}
	adapter.handleRPCMessageForGeneration(2, []byte(`{"id":1,"result":{"ok":true}}`))
	if err := <-requestDone; err != nil {
		t.Fatalf("replacement request failed: %v", err)
	}
}

func TestCodexStaleGenerationMessagesAreDiscarded(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.mu.Lock()
	adapter.cmd = &exec.Cmd{Process: &os.Process{Pid: 9201}}
	adapter.generation = 2
	adapter.mu.Unlock()
	adapter.handleNotificationForGeneration(2, "turn/started", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-new"}`))
	adapter.handleNotificationForGeneration(1, "turn/started", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-old"}`))
	if got := adapter.ActiveTurn("thread-1"); got != "turn-new" {
		t.Fatalf("stale start changed active turn=%q", got)
	}
	adapter.handleNotificationForGeneration(1, "turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-old","status":"completed"}}`))
	if got := adapter.ActiveTurn("thread-1"); got != "turn-new" {
		t.Fatalf("stale completion changed active turn=%q", got)
	}
	adapter.handleRPCMessageForGeneration(1, []byte(`{"id":"old-request","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-old","questions":[{"id":"choice"}]}}`))
	if got := adapter.PendingRequests("thread-1"); len(got) != 0 {
		t.Fatalf("stale server request was queued: %#v", got)
	}
	adapter.handleRPCMessageForGeneration(2, []byte(`{"id":"new-request","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-new","questions":[{"id":"choice"}]}}`))
	if got := adapter.PendingRequests("thread-1"); len(got) != 1 || got[0]["requestId"] != "new-request" {
		t.Fatalf("current server request=%#v", got)
	}
}

func TestCodexNotificationRevalidatesAfterLifecycleHandoff(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	oldCmd := &exec.Cmd{Process: &os.Process{Pid: 9251}}
	oldDone := make(chan struct{})
	newCmd := &exec.Cmd{Process: &os.Process{Pid: 9252}}
	newDone := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	unloadCalled := make(chan struct{}, 1)
	adapter.mu.Lock()
	adapter.cmd = oldCmd
	adapter.processDone = oldDone
	adapter.generation = 1
	adapter.mu.Unlock()
	adapter.eventMu.Lock()
	adapter.activeTurns["thread-1"] = "turn-old"
	adapter.activeTurnGeneration["thread-1"] = 1
	adapter.eventMu.Unlock()
	adapter.notificationBeforeCommit = func(generation uint64) {
		if generation != 1 {
			return
		}
		close(entered)
		<-release
	}
	adapter.unloadThreadOverride = func(_ context.Context, sessionID string) error {
		if sessionID == "thread-1" {
			unloadCalled <- struct{}{}
		}
		return nil
	}
	handlerDone := make(chan struct{})
	go func() {
		adapter.handleNotificationForGeneration(1, "turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-old","status":"completed"}}`))
		close(handlerDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old notification did not reach the lifecycle handoff")
	}
	adapter.finishProcess(oldCmd, 1, oldDone, errors.New("old generation exited"))
	adapter.mu.Lock()
	adapter.cmd = newCmd
	adapter.processDone = newDone
	adapter.generation = 2
	adapter.quarantined = false
	adapter.mu.Unlock()
	adapter.eventMu.Lock()
	adapter.activeTurns["thread-1"] = "turn-new"
	adapter.activeTurnGeneration["thread-1"] = 2
	eventsBefore := len(adapter.events)
	adapter.eventMu.Unlock()
	close(release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("old notification did not finish after handoff")
	}
	if got := adapter.ActiveTurn("thread-1"); got != "turn-new" {
		t.Fatalf("old notification changed replacement active turn=%q", got)
	}
	adapter.eventMu.Lock()
	newEvents := append([]AgentEvent(nil), adapter.events[eventsBefore:]...)
	adapter.eventMu.Unlock()
	for _, event := range newEvents {
		if event.Type == "turn.completed" && event.TurnID == "turn-old" {
			t.Fatalf("old completion was recorded after handoff: %#v", event)
		}
	}
	select {
	case <-unloadCalled:
		t.Fatal("old completion started unload after handoff")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCodexGenerationBoundUnloadCannotRemoveReplacementLoad(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	oldCmd := &exec.Cmd{Process: &os.Process{Pid: 9261}}
	oldDone := make(chan struct{})
	newCmd := &exec.Cmd{Process: &os.Process{Pid: 9262}}
	newDone := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	unsubscribed := make(chan struct{}, 1)
	adapter.mu.Lock()
	adapter.cmd = oldCmd
	adapter.processDone = oldDone
	adapter.generation = 1
	adapter.loaded["thread-1"] = struct{}{}
	adapter.loadedGeneration["thread-1"] = 1
	adapter.mu.Unlock()
	adapter.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		if method == "thread/unsubscribe" {
			unsubscribed <- struct{}{}
		}
		return map[string]any{}, nil
	}
	adapter.unloadBeforeSend = func(target codexProcess) {
		if target.generation == 1 {
			close(entered)
			<-release
		}
	}
	adapter.eventMu.Lock()
	adapter.activeTurns["thread-1"] = "turn-1"
	adapter.activeTurnGeneration["thread-1"] = 1
	adapter.eventMu.Unlock()
	adapter.handleNotificationForGeneration(1, "turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("generation-bound unload did not reach pre-send barrier")
	}
	adapter.finishProcess(oldCmd, 1, oldDone, errors.New("old generation exited"))
	adapter.mu.Lock()
	adapter.cmd = newCmd
	adapter.stdin = &bufferWriteCloser{}
	adapter.processDone = newDone
	adapter.generation = 2
	adapter.quarantined = false
	adapter.mu.Unlock()
	adapter.markThreadLoaded("thread-1")
	close(release)
	time.Sleep(20 * time.Millisecond)
	select {
	case <-unsubscribed:
		t.Fatal("old generation sent unsubscribe after replacement load")
	default:
	}
	adapter.mu.Lock()
	loaded := adapter.loadedForGenerationLocked("thread-1", 2)
	adapter.mu.Unlock()
	if !loaded {
		t.Fatal("replacement generation loaded state was removed")
	}
}

func TestCodexBlockedOldServerReplyDoesNotHoldLifecycle(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	oldCmd := &exec.Cmd{Process: &os.Process{Pid: 9271}}
	oldDone := make(chan struct{})
	oldWriter := &blockingWriteCloser{started: make(chan struct{}), release: make(chan struct{})}
	adapter.mu.Lock()
	adapter.cmd = oldCmd
	adapter.stdin = oldWriter
	adapter.processDone = oldDone
	adapter.generation = 1
	adapter.mu.Unlock()
	handlerDone := make(chan struct{})
	go func() {
		adapter.handleServerRequestForGeneration(1, json.RawMessage(`null`), "item/tool/requestUserInput", nil)
		close(handlerDone)
	}()
	select {
	case <-oldWriter.started:
	case <-time.After(time.Second):
		t.Fatal("invalid request did not enter blocked old-generation reply")
	}
	finished := make(chan struct{})
	go func() {
		adapter.finishProcess(oldCmd, 1, oldDone, errors.New("old generation exited"))
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("finishProcess blocked behind old server reply writer")
	}
	newCmd := &exec.Cmd{Process: &os.Process{Pid: 9272}}
	newDone := make(chan struct{})
	newWriter := &bufferWriteCloser{}
	adapter.mu.Lock()
	adapter.cmd = newCmd
	adapter.stdin = newWriter
	adapter.processDone = newDone
	adapter.generation = 2
	adapter.quarantined = false
	adapter.mu.Unlock()
	close(oldWriter.release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("old blocked reply did not finish after release")
	}
	if newWriter.Len() != 0 {
		t.Fatal("old server reply wrote to replacement stdin")
	}
}

func TestCodexQuarantineStopUsesSingleDeadline(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.mu.Lock()
	adapter.cmd = &exec.Cmd{Process: &os.Process{Pid: 9301}}
	adapter.processDone = make(chan struct{})
	adapter.generation = 1
	adapter.mu.Unlock()
	adapter.stopProcessOverride = func(ctx context.Context, _ *exec.Cmd) error {
		<-ctx.Done()
		return ctx.Err()
	}
	started := time.Now()
	adapter.quarantineProcess(1)
	if elapsed := time.Since(started); elapsed < codexAppServerStopWait-100*time.Millisecond || elapsed > codexAppServerStopWait+time.Second {
		t.Fatalf("quarantine stop elapsed=%s, want one ~%s budget", elapsed, codexAppServerStopWait)
	}
}

func TestCodexExitedGenerationCannotClearReplacementProcess(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	oldCmd := &exec.Cmd{}
	newCmd := &exec.Cmd{}
	oldDone := make(chan struct{})
	newDone := make(chan struct{})
	oldPending := make(chan codexRPCMessage, 1)
	newPending := make(chan codexRPCMessage, 1)
	newStdin := &bufferWriteCloser{}

	adapter.mu.Lock()
	adapter.cmd = newCmd
	adapter.stdin = newStdin
	adapter.processDone = newDone
	adapter.generation = 2
	adapter.pending[1] = codexPending{ch: oldPending, generation: 1}
	adapter.pending[2] = codexPending{ch: newPending, generation: 2}
	adapter.loaded["new-thread"] = struct{}{}
	adapter.mu.Unlock()
	adapter.serverMu.Lock()
	adapter.serverRequests["new-request"] = codexServerRequest{RequestID: "new-request"}
	adapter.serverMu.Unlock()
	adapter.eventMu.Lock()
	adapter.activeTurns["new-thread"] = "new-turn"
	adapter.eventMu.Unlock()

	adapter.finishProcess(oldCmd, 1, oldDone, errors.New("old process exited"))

	select {
	case <-oldDone:
	default:
		t.Fatal("exited generation completion was not closed")
	}
	select {
	case <-newDone:
		t.Fatal("exited generation closed the replacement process completion")
	default:
	}
	select {
	case response := <-oldPending:
		if response.Error == nil || response.Error.Code != -1 {
			t.Fatalf("old generation pending response=%#v", response)
		}
	default:
		t.Fatal("old generation pending request was not released")
	}
	select {
	case response := <-newPending:
		t.Fatalf("replacement generation pending request was released: %#v", response)
	default:
	}

	adapter.mu.Lock()
	if adapter.cmd != newCmd || adapter.stdin != newStdin || adapter.processDone != newDone || adapter.generation != 2 {
		t.Fatalf("replacement process state was changed: cmd=%p stdin=%p done=%p generation=%d", adapter.cmd, adapter.stdin, adapter.processDone, adapter.generation)
	}
	if _, ok := adapter.pending[1]; ok {
		t.Fatal("old generation pending request remained registered")
	}
	if _, ok := adapter.pending[2]; !ok {
		t.Fatal("replacement generation pending request was removed")
	}
	if _, ok := adapter.loaded["new-thread"]; !ok {
		t.Fatal("replacement generation loaded-thread state was cleared")
	}
	adapter.mu.Unlock()
	if active := adapter.ActiveTurn("new-thread"); active != "new-turn" {
		t.Fatalf("replacement generation active turn=%q", active)
	}
	adapter.serverMu.Lock()
	_, requestPresent := adapter.serverRequests["new-request"]
	adapter.serverMu.Unlock()
	if !requestPresent {
		t.Fatal("replacement generation server request was cleared")
	}
}

func TestCodexInterruptRejectsInactiveOrMismatchedTurnWithoutRPC(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	if err := adapter.InterruptTurn(context.Background(), "session-1", "old-turn"); !errors.Is(err, node.ErrAgentSessionNotFound) {
		t.Fatalf("inactive interrupt error=%v", err)
	}
	adapter.eventMu.Lock()
	adapter.activeTurns["session-1"] = "active-turn"
	adapter.eventMu.Unlock()
	if err := adapter.InterruptTurn(context.Background(), "session-1", "old-turn"); !errors.Is(err, node.ErrAgentSessionNotFound) {
		t.Fatalf("mismatched interrupt error=%v", err)
	}
}

func TestCodexInterruptRetriesTurnMaterializationRace(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	adapter.eventMu.Lock()
	adapter.activeTurns["session-1"] = "active-turn"
	adapter.eventMu.Unlock()
	calls := 0
	adapter.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		calls++
		if method != "turn/interrupt" || params["threadId"] != "session-1" || params["turnId"] != "active-turn" {
			t.Fatalf("unexpected request method=%s params=%#v", method, params)
		}
		if calls == 1 {
			return nil, &codexRPCResponseError{ExecutionError: newExecutionError("codex", method, "turn is not ready"), code: -32600}
		}
		return map[string]any{}, nil
	}

	if err := adapter.InterruptTurn(context.Background(), "session-1", "active-turn"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("request calls=%d want=2", calls)
	}
}

func TestCodexThreadStartParamsUsesProjectRootWithoutLosingWorktree(t *testing.T) {
	projectDirectory := filepath.Join(string(filepath.Separator), "repos", "project")
	workingDirectory := filepath.Join(string(filepath.Separator), "worktrees", "feature")
	params := codexThreadStartParams(workingDirectory, projectDirectory, "gpt-test", "high")
	if got := mapAnyString(params, "cwd"); got != workingDirectory {
		t.Fatalf("cwd=%q want %q", got, workingDirectory)
	}
	roots, _ := params["runtimeWorkspaceRoots"].([]string)
	if len(roots) != 2 || roots[0] != projectDirectory || roots[1] != workingDirectory {
		t.Fatalf("runtimeWorkspaceRoots=%#v", roots)
	}
	if _, ok := params["mode"]; ok {
		t.Fatal("unsupported mode field was sent")
	}
	if _, ok := params["threadStartKind"]; ok {
		t.Fatal("unsupported threadStartKind field was sent")
	}
}

func TestCodexThreadStartParamsDeduplicatesProjectRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repos", "project")
	params := codexThreadStartParams(root, root, "", "")
	roots, _ := params["runtimeWorkspaceRoots"].([]string)
	if len(roots) != 1 || roots[0] != root {
		t.Fatalf("runtimeWorkspaceRoots=%#v", roots)
	}
}

func TestBuildAgentTurnInputsPreservesNativeInputTypes(t *testing.T) {
	inputs := buildAgentTurnInputs("hello", []agentSkillInput{{Name: "demo", Path: "/skills/demo"}}, []string{"https://example.com/a.png"}, []string{"/tmp/a.png"}, []agentMentionInput{{Name: "ref", Path: "/tmp/ref.md"}}, "/tmp")
	if len(inputs) != 5 {
		t.Fatalf("inputs=%d", len(inputs))
	}
	want := []string{"text", "skill", "image", "localImage", "mention"}
	for i, typ := range want {
		if got := inputs[i]["type"]; got != typ {
			t.Fatalf("input[%d] type=%v want %s", i, got, typ)
		}
	}
	if inputs[1]["path"] != "/skills/demo" || inputs[1]["name"] != "demo" {
		t.Fatalf("skill input=%#v", inputs[1])
	}
}

func TestBuildAgentTurnInputsAddsNativeImageDetail(t *testing.T) {
	inputs := buildAgentTurnInputsWithDetail("", nil, []string{"https://example.com/a.png"}, []string{"/tmp/a.png"}, nil, "high")
	if len(inputs) != 2 {
		t.Fatalf("inputs=%d", len(inputs))
	}
	for i, input := range inputs {
		if input["detail"] != "high" {
			t.Fatalf("input[%d] detail=%v want high", i, input["detail"])
		}
	}
}

func TestCodexServerRequestResponsesStayBounded(t *testing.T) {
	userInput := codexServerRequest{
		Method:    "item/tool/requestUserInput",
		SessionID: "thread-1",
		Params: map[string]any{"questions": []any{
			map[string]any{"id": "choice"},
		}},
	}
	result, state, err := codexServerRequestResponse(userInput, agentControlParams{Answers: map[string][]string{"choice": {"A"}}})
	if err != nil || state != "answered" {
		t.Fatalf("user input response state=%q err=%v", state, err)
	}
	wantAnswers := map[string]any{"answers": map[string]any{"choice": map[string]any{"answers": []string{"A"}}}}
	if !reflect.DeepEqual(result, wantAnswers) {
		t.Fatalf("user input result=%#v want %#v", result, wantAnswers)
	}
	if _, _, err := codexServerRequestResponse(userInput, agentControlParams{Answers: map[string][]string{"unknown": {"A"}}}); err == nil {
		t.Fatal("unknown request_user_input question was accepted")
	}

	approval := codexServerRequest{Method: "item/commandExecution/requestApproval"}
	if result, state, err := codexServerRequestResponse(approval, agentControlParams{Decision: "accept"}); err != nil || state != "accept" || result["decision"] != "accept" {
		t.Fatalf("approval result=%#v state=%q err=%v", result, state, err)
	}
	if _, _, err := codexServerRequestResponse(approval, agentControlParams{Decision: "acceptForSession"}); err == nil {
		t.Fatal("session-wide approval widening was accepted")
	}

	form := codexServerRequest{Method: "mcpServer/elicitation/request", Params: map[string]any{"mode": "form"}}
	content := map[string]any{"region": "eu"}
	result, state, err = codexServerRequestResponse(form, agentControlParams{Decision: "accept", ResponseContent: content})
	if err != nil || state != "accept" || !reflect.DeepEqual(result["content"], content) {
		t.Fatalf("mcp elicitation result=%#v state=%q err=%v", result, state, err)
	}
}

func TestCodexServerRequestIDsAndTypes(t *testing.T) {
	if got, err := codexRequestIDString(json.RawMessage(`42`)); err != nil || got != "42" {
		t.Fatalf("numeric request id=%q err=%v", got, err)
	}
	if got, err := codexRequestIDString(json.RawMessage(`"req-1"`)); err != nil || got != "req-1" {
		t.Fatalf("string request id=%q err=%v", got, err)
	}
	if typ, ok := codexServerRequestType("item/tool/requestUserInput"); !ok || typ != "user_input.requested" {
		t.Fatalf("requestUserInput type=%q ok=%v", typ, ok)
	}
	if typ, ok := codexServerRequestType("item/permissions/requestApproval"); ok || typ != "permission.requested" {
		t.Fatalf("permission request type=%q ok=%v", typ, ok)
	}
}

func TestCodexAdapterQueuesAndResolvesInteractiveServerRequest(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"id":"choice","header":"Choice","question":"Pick one"}]}`)
	adapter.handleServerRequest(json.RawMessage(`"req-1"`), "item/tool/requestUserInput", params)

	adapter.serverMu.Lock()
	pending, ok := adapter.serverRequests["req-1"]
	adapter.serverMu.Unlock()
	if !ok || pending.SessionID != "thread-1" || pending.TurnID != "turn-1" {
		t.Fatalf("pending request=%#v ok=%v", pending, ok)
	}
	if snapshot := adapter.PendingRequests("thread-1"); len(snapshot) != 1 || snapshot[0]["requestId"] != "req-1" {
		t.Fatalf("pending snapshot=%#v", snapshot)
	}
	events, _, _, err := adapter.Watch(context.Background(), "thread-1", 0, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("watch events=%#v err=%v", events, err)
	}
	last := events[len(events)-1]
	if last.Type != "user_input.requested" || last.RequestID != "req-1" || last.State != "pending" {
		t.Fatalf("interactive event=%#v", last)
	}

	adapter.handleNotification("serverRequest/resolved", json.RawMessage(`{"threadId":"thread-1","requestId":"req-1"}`))
	adapter.serverMu.Lock()
	_, stillPending := adapter.serverRequests["req-1"]
	adapter.serverMu.Unlock()
	if stillPending {
		t.Fatal("resolved server request remained pending")
	}
}

type bufferWriteCloser struct{ bytes.Buffer }

func (w *bufferWriteCloser) Close() error { return nil }

type trackingWriteCloser struct {
	mu sync.Mutex
	bytes.Buffer
	written chan struct{}
	closes  int32
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.written != nil {
		select {
		case <-w.written:
		default:
			close(w.written)
		}
	}
	return w.Buffer.Write(p)
}

func (w *trackingWriteCloser) Close() error {
	atomic.AddInt32(&w.closes, 1)
	return nil
}

type blockingWriteCloser struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int32
}

func (w *blockingWriteCloser) Write(p []byte) (int, error) {
	atomic.AddInt32(&w.calls, 1)
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func (w *blockingWriteCloser) Close() error { return nil }

type failOnceWriteCloser struct {
	bufferWriteCloser
	calls int32
}

func (w *failOnceWriteCloser) Write(p []byte) (int, error) {
	if atomic.AddInt32(&w.calls, 1) == 1 {
		return 0, errors.New("injected write failure")
	}
	return w.bufferWriteCloser.Write(p)
}

func TestCodexAdapterRespondPendingRequestWritesJSONRPCResponse(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &bufferWriteCloser{}
	adapter.mu.Lock()
	adapter.stdin = writer
	adapter.mu.Unlock()
	adapter.handleServerRequest(json.RawMessage(`7`), "item/tool/requestUserInput", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","questions":[{"id":"choice"}]}`))
	result, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "7", agentControlParams{Answers: map[string][]string{"choice": {"A"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result["responded"] != true {
		t.Fatalf("respond result=%#v", result)
	}
	var message map[string]any
	line := strings.TrimSpace(writer.String())
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		t.Fatalf("invalid JSON-RPC response %q: %v", line, err)
	}
	if id, _ := message["id"].(float64); id != 7 {
		t.Fatalf("response id=%v", message["id"])
	}
	response, _ := message["result"].(map[string]any)
	answers, _ := response["answers"].(map[string]any)
	if _, ok := answers["choice"]; !ok {
		t.Fatalf("response answers=%#v", response)
	}
	adapter.serverMu.Lock()
	_, pending := adapter.serverRequests["7"]
	adapter.serverMu.Unlock()
	if pending {
		t.Fatal("responded request remained pending")
	}
}

func TestCodexAdapterRespondPendingRequestClaimsRequestBeforeBlockingWrite(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &blockingWriteCloser{started: make(chan struct{}), release: make(chan struct{})}
	adapter.mu.Lock()
	adapter.stdin = writer
	adapter.mu.Unlock()
	adapter.handleServerRequest(json.RawMessage(`9`), "item/commandExecution/requestApproval", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1"}`))

	firstDone := make(chan error, 1)
	go func() {
		_, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "9", agentControlParams{Decision: "accept"})
		firstDone <- err
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("first response did not reach the blocking writer")
	}
	if snapshot := adapter.PendingRequests("thread-1"); len(snapshot) != 0 {
		t.Fatalf("claimed request remained visible as pending: %#v", snapshot)
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "9", agentControlParams{Decision: "decline"})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err == nil || !strings.Contains(err.Error(), "already being responded to") {
			t.Fatalf("concurrent response error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent response blocked behind the JSON-RPC writer")
	}
	close(writer.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first response failed: %v", err)
	}
	if calls := atomic.LoadInt32(&writer.calls); calls != 1 {
		t.Fatalf("JSON-RPC writes=%d want=1", calls)
	}
}

func TestCodexAdapterRespondPendingRequestRestoresClaimAfterWriteFailure(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &failOnceWriteCloser{}
	adapter.mu.Lock()
	adapter.stdin = writer
	adapter.mu.Unlock()
	adapter.handleServerRequest(json.RawMessage(`11`), "item/commandExecution/requestApproval", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1"}`))

	if _, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "11", agentControlParams{Decision: "accept"}); err == nil {
		t.Fatal("injected JSON-RPC write failure unexpectedly succeeded")
	}
	if snapshot := adapter.PendingRequests("thread-1"); len(snapshot) != 1 || snapshot[0]["requestId"] != "11" {
		t.Fatalf("failed response was not restored for retry: %#v", snapshot)
	}
	result, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "11", agentControlParams{Decision: "accept"})
	if err != nil || result["responded"] != true {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
	if calls := atomic.LoadInt32(&writer.calls); calls != 2 {
		t.Fatalf("JSON-RPC writes=%d want=2 attempts", calls)
	}
}

func TestNormalizeMCPStatusDropsToolSchemas(t *testing.T) {
	result := normalizeMCPStatus(map[string]any{
		"data": []any{map[string]any{
			"name":       "demo",
			"authStatus": "oAuth",
			"tools": map[string]any{
				"zeta":  map[string]any{"inputSchema": map[string]any{"type": "object"}},
				"alpha": map[string]any{"inputSchema": map[string]any{"type": "object"}},
			},
			"resources": []any{map[string]any{"name": "doc", "uri": "demo://doc", "secret": "drop-me"}},
		}},
		"nextCursor": "next",
	})
	servers, _ := result["servers"].([]map[string]any)
	if len(servers) != 1 {
		t.Fatalf("servers=%#v", result)
	}
	if !reflect.DeepEqual(servers[0]["tools"], []string{"alpha", "zeta"}) {
		t.Fatalf("tool summary=%#v", servers[0]["tools"])
	}
	resources, _ := servers[0]["resources"].([]map[string]any)
	if len(resources) != 1 || resources[0]["uri"] != "demo://doc" {
		t.Fatalf("resource summary=%#v", resources)
	}
	if _, leaked := resources[0]["secret"]; leaked {
		t.Fatal("resource summary leaked unapproved fields")
	}
}

func TestCCSwitchModelExtractionAndRoutingRules(t *testing.T) {
	claudeSettings := map[string]any{"env": map[string]any{
		"ANTHROPIC_MODEL":                "deepseek-v4-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek-v4-pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek-v4-flash",
	}}
	models := extractCCSwitchModels("claude", claudeSettings, map[string]any{})
	if len(models) != 4 {
		t.Fatalf("claude role models=%#v", models)
	}
	if got := models[0]["model"]; got == "" {
		t.Fatalf("claude model missing: %#v", models[0])
	}

	desktopMeta := map[string]any{"claudeDesktopModelRoutes": map[string]any{
		"claude-sonnet-5": map[string]any{"model": "gpt-5.6-terra", "labelOverride": "GPT-5.6 Terra"},
		"claude-opus-5":   map[string]any{"model": "gpt-5.6-sol", "labelOverride": "GPT-5.6 Sol", "supports1m": true},
	}}
	desktopModels := extractCCSwitchModels("claude-desktop", map[string]any{}, desktopMeta)
	if len(desktopModels) != 2 {
		t.Fatalf("desktop model routes=%#v", desktopModels)
	}
	foundLabel := false
	for _, model := range desktopModels {
		if model["model"] == "gpt-5.6-terra" && model["displayName"] == "GPT-5.6 Terra" {
			foundLabel = true
		}
	}
	if !foundLabel {
		t.Fatalf("desktop label override not preserved: %#v", desktopModels)
	}

	codexSettings := map[string]any{"config": "model = \"gpt-5.6-sol\"\nreview_model = \"gpt-5.6-terra\"\nmodel_provider = \"custom\"\nwire_api = \"responses\"\n[model_providers.custom]\nbase_url = \"https://example.invalid\"\n"}
	codexModels := extractCCSwitchModels("codex", codexSettings, map[string]any{})
	if len(codexModels) != 2 {
		t.Fatalf("codex config models=%#v", codexModels)
	}
	fields := parseCCSwitchTopLevelConfig(codexSettings["config"].(string))
	if fields["model_provider"] != "custom" || fields["wire_api"] != "responses" {
		t.Fatalf("codex top-level config=%#v", fields)
	}

	if required, known := ccSwitchNeedsRouting("claude", "openai_responses", nil); !known || !required {
		t.Fatalf("Claude OpenAI Responses must require local routing: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("claude", "anthropic", nil); !known || required {
		t.Fatalf("Claude Anthropic format should not require conversion: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("codex", "openai_responses", nil); !known || required {
		t.Fatalf("Codex Responses should not require conversion: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("codex", "openai_chat", nil); !known || !required {
		t.Fatalf("Codex Chat must require local routing: required=%v known=%v", required, known)
	}

	if got := ccSwitchRoutingMode(map[string]any{"takeoverEnabled": false, "liveTakeoverActive": false}, map[string]any{}); got != "direct" {
		t.Fatalf("routingMode=%q want direct", got)
	}
	if got := ccSwitchRoutingMode(map[string]any{"takeoverEnabled": true, "liveTakeoverActive": false}, map[string]any{}); got != "cc_switch" {
		t.Fatalf("routingMode=%q want cc_switch", got)
	}

	caps := deriveCCSwitchEffectiveCapabilities("claude", "cc_switch", map[string]any{"providerId": "third-party", "category": "custom", "apiFormat": "openai_responses"})
	web, _ := caps["webSearch"].(map[string]any)
	if web["state"] != "unsupported" {
		t.Fatalf("routed Claude webSearch=%#v", web)
	}
}

func TestCCSwitchSanitizersExposeNoCredentials(t *testing.T) {
	settings := map[string]any{
		"apiKey": "super-secret",
		"nested": map[string]any{"access_token": "token-value"},
	}
	if !ccSwitchCredentialPresent(settings) {
		t.Fatal("credential presence was not detected")
	}
	if host := ccSwitchEndpointHost("https://user:pass@example.com:8443/v1/messages?token=secret"); host != "example.com:8443" {
		t.Fatalf("endpoint host=%q", host)
	}
}

func TestClaudeCodeStreamParserAndSessionIndex(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	sessionID := "11111111-2222-4333-8444-555555555555"
	record := &ClaudeSessionRecord{
		SessionID:        sessionID,
		WorkingDirectory: dataDir,
		Status:           "running",
		CreatedAt:        protocolTimestampNow(),
		UpdatedAt:        protocolTimestampNow(),
	}
	adapter.mu.Lock()
	adapter.sessions[sessionID] = record
	adapter.mu.Unlock()

	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"system","subtype":"init","session_id":"11111111-2222-4333-8444-555555555555","model":"claude-sonnet-5"}`))
	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Read"}]}}`))
	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":12,"num_turns":1,"usage":{"input_tokens":3,"output_tokens":2}}`))

	result, err := adapter.Result(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "completed" || result["finalAgentMessage"] != "done" || result["nativeModel"] != "claude-sonnet-5" {
		t.Fatalf("unexpected Claude result: %#v", result)
	}
	events, _, _, err := adapter.Watch(context.Background(), sessionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenAssistant := false
	seenTool := false
	for _, event := range events {
		if event.Type == "assistant.message" && event.Text == "hello" {
			seenAssistant = true
		}
		if event.Type == "tool.started" && event.Text == "Read" {
			seenTool = true
		}
	}
	if !seenAssistant || !seenTool {
		t.Fatalf("Claude normalized events=%#v", events)
	}

	adapter.mu.Lock()
	if _, err := adapter.saveIndexLocked(); err != nil {
		adapter.mu.Unlock()
		t.Fatal(err)
	}
	adapter.mu.Unlock()
	reloaded := NewClaudeCodeAdapter(dataDir, nil, nil)
	got, err := reloaded.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := got["session"].(map[string]any)
	if session["nativeModel"] != "claude-sonnet-5" || session["status"] != "completed" {
		t.Fatalf("reloaded Claude session=%#v", session)
	}
}

func TestClaudeActualUpstreamRequiresExactSessionCorrelation(t *testing.T) {
	route := map[string]any{
		"routingMode": "cc_switch",
		"lastRequest": map[string]any{
			"sessionId":     "session-a",
			"providerId":    "route-provider",
			"upstreamModel": "deepseek-v4-pro",
			"requestModel":  "sonnet",
		},
	}
	if got := claudeActualUpstream("session-b", route); got != nil {
		t.Fatalf("mismatched session correlated upstream=%#v", got)
	}
	got := claudeActualUpstream("session-a", route)
	if got["providerId"] != "route-provider" || got["upstreamModel"] != "deepseek-v4-pro" {
		t.Fatalf("correlated upstream=%#v", got)
	}
	if got := claudeActualUpstream("session-a", map[string]any{"routingMode": "direct", "lastRequest": route["lastRequest"]}); got != nil {
		t.Fatalf("direct route should not claim upstream correlation: %#v", got)
	}
}

func TestClaudeTurnInputValidationIsProviderSpecific(t *testing.T) {
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Thinking: "max"}); err != nil {
		t.Fatalf("valid Claude effort rejected: %v", err)
	}
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Thinking: "ultra"}); err == nil {
		t.Fatal("unknown Claude effort accepted")
	}
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Skills: []agentSkillInput{{Name: "demo", Path: "/tmp/demo"}}}); err == nil {
		t.Fatal("Claude provider silently accepted an unsupported native Skill input")
	}
	if err := validateClaudeTurnInput(agentControlParams{}); err == nil {
		t.Fatal("Claude provider accepted an empty turn")
	}
}

func TestCodexNativeControlParamsMatchAppServerSchema(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "repos", "project")
	tests := []struct {
		name string
		got  map[string]any
		want map[string]any
	}{
		{
			name: "skills list uses cwds",
			got:  codexSkillsListParams(cwd, true),
			want: map[string]any{"cwds": []string{cwd}, "forceReload": true},
		},
		{
			name: "plugin list uses protocol filters",
			got:  codexPluginListParams(cwd, []string{"local", "workspace-directory"}),
			want: map[string]any{"cwds": []string{cwd}, "marketplaceKinds": []string{"local", "workspace-directory"}},
		},
		{
			name: "plugin read uses plugin name",
			got:  codexPluginReadParams("documents", cwd, "openai-primary-runtime"),
			want: map[string]any{"pluginName": "documents", "marketplacePath": cwd, "remoteMarketplaceName": "openai-primary-runtime"},
		},
		{
			name: "plugin skill read uses remote identifiers",
			got:  codexPluginSkillReadParams("openai-curated", "remote-123", "review"),
			want: map[string]any{"remoteMarketplaceName": "openai-curated", "remotePluginId": "remote-123", "skillName": "review"},
		},
		{
			name: "rollback uses numTurns",
			got:  codexRollbackParams("thread-1", 3),
			want: map[string]any{"threadId": "thread-1", "numTurns": 3},
		},
		{
			name: "goal set preserves typed fields",
			got:  codexGoalSetParams("thread-1", "ship release", "active", 50000),
			want: map[string]any{"threadId": "thread-1", "objective": "ship release", "status": "active", "tokenBudget": int64(50000)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("params=%#v want %#v", tt.got, tt.want)
			}
		})
	}
}

func TestCodexSettingsAndReviewParamsMatchAppServerSchema(t *testing.T) {
	settings := agentControlParams{
		WorkingDirectory: "/repo",
		Model:            "gpt-5.5",
		Effort:           "high",
		Permissions:      "workspace-write",
		Personality:      "pragmatic",
		ServiceTier:      "priority",
		Summary:          "concise",
	}
	gotSettings := codexSettingsUpdateParams("thread-1", settings)
	wantSettings := map[string]any{
		"threadId": "thread-1", "cwd": "/repo", "model": "gpt-5.5", "effort": "high",
		"permissions": "workspace-write", "personality": "pragmatic", "serviceTier": "priority", "summary": "concise",
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Fatalf("settings=%#v want %#v", gotSettings, wantSettings)
	}

	tests := []struct {
		name  string
		input agentControlParams
		want  map[string]any
	}{
		{"default", agentControlParams{}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "uncommittedChanges"}}},
		{"base branch", agentControlParams{ReviewType: "baseBranch", ReviewDelivery: "detached", ReviewBranch: "main"}, map[string]any{"threadId": "thread-1", "delivery": "detached", "target": map[string]any{"type": "baseBranch", "branch": "main"}}},
		{"commit", agentControlParams{ReviewType: "commit", ReviewSHA: "abc123", ReviewTitle: "release"}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "commit", "sha": "abc123", "title": "release"}}},
		{"custom", agentControlParams{ReviewType: "custom", ReviewInstructions: "focus on races"}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "custom", "instructions": "focus on races"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexReviewStartParams("thread-1", tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("review=%#v want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentControlValidationMatchesCodexEnums(t *testing.T) {
	if !hasTurnInput(agentControlParams{Skills: []agentSkillInput{{Name: "demo", Path: "/skill"}}}) {
		t.Fatal("skill-only create must count as a turn input")
	}
	if err := validateGoalInput(agentControlParams{GoalStatus: "active"}); err != nil {
		t.Fatalf("valid goal status rejected: %v", err)
	}
	if err := validateGoalInput(agentControlParams{GoalStatus: "done"}); err == nil {
		t.Fatal("unknown goal status was accepted")
	}
	if err := validateGoalInput(agentControlParams{TokenBudget: -1}); err == nil {
		t.Fatal("negative token budget was accepted")
	}
	if err := validateReviewInput(agentControlParams{ReviewType: "baseBranch", ReviewBranch: "main", ReviewDelivery: "detached"}); err != nil {
		t.Fatalf("valid baseBranch review rejected: %v", err)
	}
	if err := validateReviewInput(agentControlParams{ReviewType: "commit"}); err == nil {
		t.Fatal("commit review without sha was accepted")
	}
	if err := validateMarketplaceKinds([]string{"local", "workspace-directory"}); err != nil {
		t.Fatalf("valid marketplace kinds rejected: %v", err)
	}
	if err := validateMarketplaceKinds([]string{"marketplace"}); err == nil {
		t.Fatal("unknown marketplace kind was accepted")
	}
}

func TestValidateOutputSchemaBounds(t *testing.T) {
	if err := validateOutputSchema(map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}); err != nil {
		t.Fatal(err)
	}
	deep := map[string]any{}
	current := deep
	for i := 0; i < 14; i++ {
		next := map[string]any{}
		current["x"] = next
		current = next
	}
	if err := validateOutputSchema(deep); err == nil {
		t.Fatal("expected depth error")
	}
}

type concurrentLineWriter struct {
	mu     sync.Mutex
	bytes  bytes.Buffer
	active int32
}

func (w *concurrentLineWriter) Write(p []byte) (int, error) {
	if !atomic.CompareAndSwapInt32(&w.active, 0, 1) {
		return 0, fmt.Errorf("concurrent write")
	}
	defer atomic.StoreInt32(&w.active, 0)
	time.Sleep(time.Microsecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes.Write(p)
}

func (w *concurrentLineWriter) Lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := append([]byte(nil), w.bytes.Bytes()...)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func TestCodexThreadNotMaterializedClassification(t *testing.T) {
	if !isCodexThreadNotMaterialized(fmt.Errorf("Codex thread/read: thread abc is not materialized yet; includeTurns is unavailable before first user message")) {
		t.Fatal("expected Codex not-materialized error to be recognized")
	}
	if isCodexThreadNotMaterialized(fmt.Errorf("Codex thread/read: session not found")) {
		t.Fatal("unrelated Codex error was misclassified")
	}
}

func TestCodexRPCRejectionDistinguishesDefinitiveAndAmbiguousFailures(t *testing.T) {
	definitive := &codexRPCResponseError{ExecutionError: newExecutionError("codex", "thread/start", "invalid model"), code: -32602}
	if !isDefinitiveCodexRPCRejection(definitive) {
		t.Fatal("JSON-RPC rejection was not classified as definitive")
	}
	ambiguous := &codexRPCResponseError{ExecutionError: newExecutionError("codex", "thread/start", "Codex app-server exited"), code: -1}
	if isDefinitiveCodexRPCRejection(ambiguous) {
		t.Fatal("transport termination was classified as a definitive rejection")
	}
	if isDefinitiveCodexRPCRejection(context.DeadlineExceeded) {
		t.Fatal("request timeout was classified as a definitive rejection")
	}
}

func TestCodexAdapterWriteLineSerializesConcurrentRPCMessages(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &concurrentLineWriter{}
	const count = 128
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := adapter.writeLine(writer, map[string]any{"id": i, "text": fmt.Sprintf("message-%03d", i)}); err != nil {
				t.Errorf("writeLine(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	lines := writer.Lines()
	if len(lines) != count {
		t.Fatalf("got %d complete lines, want %d", len(lines), count)
	}
	seen := make(map[int]bool, count)
	for _, line := range lines {
		var message struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("interleaved or invalid JSON line %q: %v", line, err)
		}
		seen[message.ID] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d distinct message IDs, want %d", len(seen), count)
	}
}

var _ io.Writer = (*concurrentLineWriter)(nil)
