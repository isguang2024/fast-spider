package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
