package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProgressFrames(t *testing.T) {
	var got []string
	err := readChatGPTProgressFrames(strings.NewReader(": heartbeat\r\nevent: delta\r\ndata: {\r\ndata: \"v\":1}\r\n\r\ndata: incomplete"), func(event, data string) error { got = append(got, event+":"+data); return nil })
	if err != nil || len(got) != 1 || got[0] != "delta:{\n\"v\":1}" {
		t.Fatalf("got %v %v", got, err)
	}
	err = readChatGPTProgressFrames(strings.NewReader("data: "+strings.Repeat("x", 256*1024)), func(string, string) error { return nil })
	if err == nil {
		t.Fatal("oversize frame accepted")
	}
}

func TestProgressSharedLocalReadAndCancellation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/backend-api/f/conversation/resume" {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: delta\ndata: {\"p\":\"\",\"o\":\"add\",\"v\":{\"message\":{\"content\":\"hello\"}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	a := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "test", nil })
	a.http = srv.Client()
	a.baseURL = srv.URL
	p, err := newChatGPTCloudProgress(a, filepath.Join(t.TempDir(), "progress.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := p.snapshot(ctx, "test", 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out["source"] != "sse_local" || len(out["events"].([]map[string]any)) != 1 {
		t.Fatal(out)
	}
	for i := 0; i < 10; i++ {
		if _, err := p.snapshot(ctx, "test", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("local reads opened %d connections", calls.Load())
	}
	p.release("test", 0)
}

func TestProgressEOFDoesNotConfirmAndReplayDeduplicates(t *testing.T) {
	var confirms atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: delta\ndata: {\"v\":\"a\"}\n\ndata: {\"type\":\"stream_handoff\"}\n\n")
	}))
	defer srv.Close()
	a := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "test", nil })
	a.http = srv.Client()
	a.baseURL = srv.URL
	a.readBudget.minInterval = 0
	p, err := newChatGPTCloudProgress(a, filepath.Join(t.TempDir(), "progress.sqlite3"), func(string, int64) { confirms.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s := &chatGPTCloudProgressSession{generation: 1}
	p.store, err = newChatGPTCloudProgressStore(p.path)
	if err != nil {
		t.Fatal(err)
	}
	p.sessions["test"] = s
	for i := 0; i < 2; i++ {
		if _, err := p.connect(context.Background(), "test", s); err != nil {
			t.Fatal(err)
		}
	}
	events, _, err := p.store.Snapshot("test", 0)
	if err != nil || len(events) != 2 || confirms.Load() != 0 {
		t.Fatalf("events=%v err=%v confirms=%d", events, err, confirms.Load())
	}
}

func TestProgressDefaultWatchReportsUnavailableWithoutDetailPolling(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/backend-api/f/conversation/resume" {
			t.Errorf("unexpected detail query %s", r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	m.chatgptCloud.http = srv.Client()
	m.chatgptCloud.baseURL = srv.URL
	m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "test", nil }
	out, err := m.chatgptCloudWatch(context.Background(), agentControlParams{SessionID: "ended", WaitSeconds: 1})
	if err != nil || out["source"] != "sse_local" || out["httpStatus"] != 404 || out["connectionState"] != "unavailable" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	for i := 0; i < 10; i++ {
		if _, err := m.chatgptCloudWatch(context.Background(), agentControlParams{SessionID: "ended"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("404 retried %d times", calls.Load())
	}
}

func TestProgressTerminalHintUsesGenerationAndDeduplicates(t *testing.T) {
	var confirms atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_stream_complete\"}\n\n")
	}))
	defer srv.Close()
	a := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "test", nil })
	a.http = srv.Client()
	a.baseURL = srv.URL
	a.readBudget.minInterval = 0
	p, err := newChatGPTCloudProgress(a, filepath.Join(t.TempDir(), "progress.sqlite3"), func(id string, generation int64) {
		if id != "test" || generation != 3 {
			t.Errorf("wrong callback identity %s/%d", id, generation)
		}
		confirms.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.store, err = newChatGPTCloudProgressStore(p.path)
	if err != nil {
		t.Fatal(err)
	}
	s := &chatGPTCloudProgressSession{generation: 3}
	p.sessions["test"] = s
	for i := 0; i < 2; i++ {
		if _, err := p.connect(context.Background(), "test", s); err != nil {
			t.Fatal(err)
		}
	}
	if confirms.Load() != 1 {
		t.Fatalf("confirmations %d", confirms.Load())
	}
	delete(p.sessions, "test")
	if _, err := p.connect(context.Background(), "test", s); err == nil {
		t.Fatal("stale stream accepted")
	}
	if confirms.Load() != 1 {
		t.Fatal("stale generation confirmed")
	}
}
