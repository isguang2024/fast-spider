package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingBrowserStdin struct {
	writeStarted chan struct{}
	closed       chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
}

func (w *blockingBrowserStdin) Write([]byte) (int, error) {
	w.writeOnce.Do(func() { close(w.writeStarted) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockingBrowserStdin) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

type delayedBrowserStdinClose struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type partialFailureBrowserStdin struct {
	closed atomic.Bool
}

func (*partialFailureBrowserStdin) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return 1, io.ErrUnexpectedEOF
}

func (w *partialFailureBrowserStdin) Close() error {
	w.closed.Store(true)
	return nil
}

func (*delayedBrowserStdinClose) Write(p []byte) (int, error) { return len(p), nil }
func (w *delayedBrowserStdinClose) Close() error {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return nil
}

type framingBrowserStdin struct {
	mu         sync.Mutex
	sidecar    *BrowserSidecar
	buffer     []byte
	active     int
	overlapped bool
}

func (w *framingBrowserStdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.active++
	if w.active > 1 {
		w.overlapped = true
	}
	w.mu.Unlock()
	time.Sleep(100 * time.Microsecond)
	if len(p) > 7 {
		p = p[:7]
	}
	w.mu.Lock()
	w.buffer = append(w.buffer, p...)
	var ids []string
	for {
		newline := strings.IndexByte(string(w.buffer), '\n')
		if newline < 0 {
			break
		}
		var request struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.buffer[:newline], &request); err == nil && request.ID != "" {
			ids = append(ids, request.ID)
		}
		w.buffer = append([]byte(nil), w.buffer[newline+1:]...)
	}
	w.active--
	w.mu.Unlock()
	for _, id := range ids {
		w.sidecar.mu.Lock()
		response := w.sidecar.pending[id]
		if response != nil {
			delete(w.sidecar.pending, id)
		}
		w.sidecar.mu.Unlock()
		if response != nil {
			response <- browserSidecarResponse{ID: id, OK: true, Result: map[string]any{"ok": true}}
		}
	}
	return len(p), nil
}

func (*framingBrowserStdin) Close() error { return nil }

func TestBrowserSidecarRejectsLegacyPolicyProtocol(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"fastSpider":{"sidecarProtocol":"1.0"}}`,
		"index.mjs":    "export {};\n",
		filepath.Join("node_modules", "playwright", "package.json"): `{"name":"playwright"}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewBrowserSidecar(dir, nil).Available(); !errors.Is(err, ErrBrowserUnavailable) || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("legacy sidecar error=%v", err)
	}
}

func TestBrowserAvailabilityWaitsForHandshakeOutcome(t *testing.T) {
	sidecar := NewBrowserSidecar(t.TempDir(), nil)
	sidecar.beginStart()
	var wait sync.WaitGroup
	wait.Add(1)
	result := make(chan struct {
		status browserAvailabilityStatus
		err    error
	}, 1)
	go func() {
		defer wait.Done()
		status, err := sidecar.AvailabilityStatus()
		result <- struct {
			status browserAvailabilityStatus
			err    error
		}{status: status, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	sidecar.finishStart(errors.New("synthetic handshake failure"))
	wait.Wait()
	got := <-result
	if got.err == nil || got.status.State != "blocked" || got.status.ReasonCode != "sidecar_start_failed" {
		t.Fatalf("availability=%+v err=%v", got.status, got.err)
	}
}

func TestBrowserAvailabilityHoldersExpireAndStayBounded(t *testing.T) {
	registry := &browserAvailabilityHolders
	registry.mu.Lock()
	original := registry.entries
	registry.entries = make(map[string]browserAvailabilityHolderEntry)
	registry.mu.Unlock()
	t.Cleanup(func() {
		registry.mu.Lock()
		registry.entries = original
		registry.mu.Unlock()
	})

	started := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	first := browserAvailabilityForAt("holder-0", started)
	if same := browserAvailabilityForAt("holder-0", started.Add(time.Millisecond)); same != first {
		t.Fatal("unexpired availability holder was not reused")
	}
	for index := 1; index <= maxBrowserAvailabilityHolders; index++ {
		browserAvailabilityForAt(fmt.Sprintf("holder-%d", index), started.Add(time.Duration(index+1)*time.Millisecond))
	}
	registry.mu.Lock()
	size := len(registry.entries)
	_, retainedOldest := registry.entries[filepath.Clean("holder-0")]
	registry.mu.Unlock()
	if size != maxBrowserAvailabilityHolders {
		t.Fatalf("availability holder count=%d want=%d", size, maxBrowserAvailabilityHolders)
	}
	if retainedOldest {
		t.Fatal("bounded availability registry retained its oldest holder")
	}

	expired := browserAvailabilityForAt("holder-expired", started.Add(browserAvailabilityHolderTTL+time.Second))
	if expired == first {
		t.Fatal("expired availability holder was reused")
	}
	if sameKey := browserAvailabilityForAt("holder-0", started.Add(browserAvailabilityHolderTTL+2*time.Second)); sameKey == first {
		t.Fatal("expired same-key availability holder was reused")
	}
	registry.mu.Lock()
	size = len(registry.entries)
	registry.mu.Unlock()
	if size != 2 {
		t.Fatalf("expired holder sweep left %d entries", size)
	}
}

func TestBrowserSidecarUsesBundledNodeAndBrowserCache(t *testing.T) {
	dir := t.TempDir()
	nodeName := "node"
	if runtime.GOOS == "windows" {
		nodeName = "node.exe"
	}
	nodePath := filepath.Join(dir, nodeName)
	if err := os.WriteFile(nodePath, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	browserRoot := filepath.Join(dir, "browsers")
	if err := os.MkdirAll(browserRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	sidecar := &BrowserSidecar{dir: dir}
	resolved, err := sidecar.nodeExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(resolved) != filepath.Clean(nodePath) {
		t.Fatalf("node executable=%q want=%q", resolved, nodePath)
	}

	want := "PLAYWRIGHT_BROWSERS_PATH=" + browserRoot
	found := false
	for _, item := range browserSidecarEnvironment(dir) {
		if strings.EqualFold(item, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("browser environment omitted %q", want)
	}
}

func TestBrowserSidecarBlockedWriteHonorsContextAndReleasesStateLock(t *testing.T) {
	stdin := &blockingBrowserStdin{writeStarted: make(chan struct{}), closed: make(chan struct{})}
	sidecar := &BrowserSidecar{cmd: &exec.Cmd{}, stdin: stdin, pending: make(map[string]chan browserSidecarResponse)}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := sidecar.callStarted(ctx, "page.open", map[string]any{})
		result <- err
	}()
	select {
	case <-stdin.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("test writer was never entered")
	}
	started := time.Now()
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("blocked write did not return after context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked write error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked write ignored context for %s", elapsed)
	}
	locked := make(chan struct{})
	go func() {
		sidecar.mu.Lock()
		sidecar.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("sidecar state lock remained held by blocked stdin write")
	}
}

func TestBrowserSidecarPartialWriteFailureRetiresCorruptedStream(t *testing.T) {
	stdin := &partialFailureBrowserStdin{}
	sidecar := &BrowserSidecar{cmd: &exec.Cmd{}, stdin: stdin, pending: make(map[string]chan browserSidecarResponse)}
	_, err := sidecar.callStarted(context.Background(), "page.open", map[string]any{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial write error=%v", err)
	}
	if !stdin.closed.Load() {
		t.Fatal("partial write failure did not close the corrupted stream")
	}
	sidecar.mu.Lock()
	closed, current := sidecar.closed, sidecar.stdin
	sidecar.mu.Unlock()
	if !closed || current != nil {
		t.Fatalf("sidecar remained reusable after partial write: closed=%v stdin=%v", closed, current)
	}
}

func TestBrowserSidecarCloseIsBoundedByContext(t *testing.T) {
	stdin := &delayedBrowserStdinClose{started: make(chan struct{}), release: make(chan struct{})}
	sidecar := &BrowserSidecar{cmd: &exec.Cmd{}, stdin: stdin, pending: make(map[string]chan browserSidecarResponse)}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- sidecar.Close(ctx) }()
	select {
	case <-stdin.started:
	case <-time.After(time.Second):
		t.Fatal("stdin close was not attempted")
	}
	started := time.Now()
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("close did not return after context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("close error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("close ignored context for %s", elapsed)
	}
	close(stdin.release)
}

func TestBrowserSidecarEnsureStartedWaitsForCloseGeneration(t *testing.T) {
	inputStarted := make(chan struct{})
	inputRelease := make(chan struct{})
	killStarted := make(chan struct{})
	killRelease := make(chan struct{})
	processReaped := make(chan struct{})
	var inputCalls atomic.Int32
	var killCalls atomic.Int32
	sidecar := &BrowserSidecar{
		dir:         t.TempDir(),
		cmd:         &exec.Cmd{Process: &os.Process{Pid: 12345}},
		stdin:       &delayedBrowserStdinClose{started: make(chan struct{}), release: make(chan struct{})},
		processDone: processReaped,
		pending:     make(map[string]chan browserSidecarResponse),
		ready:       true,
		closeInput: func(io.WriteCloser) error {
			inputCalls.Add(1)
			close(inputStarted)
			<-inputRelease
			return errors.New("stdin already closed")
		},
		killTree: func(*exec.Cmd) error {
			killCalls.Add(1)
			close(killStarted)
			<-killRelease
			return errors.New("process already exited")
		},
	}
	canceledCtx, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	if err := sidecar.Close(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close error=%v", err)
	}
	for _, started := range []<-chan struct{}{inputStarted, killStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background close cleanup did not start")
		}
	}

	secondClose := make(chan error, 1)
	go func() { secondClose <- sidecar.Close(context.Background()) }()
	startCtx, cancelStart := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStart()
	startResult := make(chan error, 1)
	go func() { startResult <- sidecar.ensureStarted(startCtx) }()
	select {
	case err := <-startResult:
		t.Fatalf("ensureStarted returned before old cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(inputRelease)
	select {
	case err := <-startResult:
		t.Fatalf("ensureStarted returned before process kill: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(killRelease)
	select {
	case err := <-startResult:
		t.Fatalf("ensureStarted returned before process reap: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(processReaped)
	if err := <-secondClose; err != nil {
		t.Fatalf("concurrent close error=%v", err)
	}
	if err := <-startResult; !errors.Is(err, ErrBrowserUnavailable) {
		t.Fatalf("ensureStarted after cleanup error=%v", err)
	}
	if inputCalls.Load() != 1 || killCalls.Load() != 1 {
		t.Fatalf("cleanup calls input=%d kill=%d", inputCalls.Load(), killCalls.Load())
	}
	sidecar.mu.Lock()
	closing, cmd, stdin, processDone := sidecar.closing, sidecar.cmd, sidecar.stdin, sidecar.processDone
	sidecar.mu.Unlock()
	if closing || cmd != nil || stdin != nil || processDone != nil {
		t.Fatalf("cleanup state closing=%v cmd=%v stdin=%v processDone=%v", closing, cmd, stdin, processDone)
	}
}

func TestBrowserSidecarConcurrentCallsWriteCompleteFrames(t *testing.T) {
	sidecar := &BrowserSidecar{cmd: &exec.Cmd{}, pending: make(map[string]chan browserSidecarResponse)}
	stdin := &framingBrowserStdin{sidecar: sidecar}
	sidecar.stdin = stdin
	const callers = 12
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := sidecar.callStarted(ctx, "page.open", map[string]any{"index": index})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent call error=%v", err)
		}
	}
	stdin.mu.Lock()
	defer stdin.mu.Unlock()
	if stdin.overlapped {
		t.Fatal("concurrent calls overlapped stdin writes")
	}
	if len(stdin.buffer) != 0 {
		t.Fatalf("incomplete frame bytes=%d", len(stdin.buffer))
	}
}
