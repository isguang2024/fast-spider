package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Progress is a disposable observation, never a completion receipt. The provider
// offset contract is not documented: reconnects replay offset zero and dedupe.
type chatGPTCloudProgress struct {
	mu       sync.Mutex
	adapter  *ChatGPTCloudAdapter
	store    *chatGPTCloudProgressStore
	path     string
	cancel   context.CancelFunc
	ctx      context.Context
	wg       sync.WaitGroup
	sessions map[string]*chatGPTCloudProgressSession
	confirm  func(string, int64)
}
type chatGPTCloudProgressSession struct {
	cancel       context.CancelFunc
	generation   int64
	persistent   bool
	touched      time.Time
	state        string
	nextRetry    time.Time
	lastProgress time.Time
	progressKey  string
	httpStatus   int
}

func newChatGPTCloudProgress(a *ChatGPTCloudAdapter, path string, confirm func(string, int64)) (*chatGPTCloudProgress, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &chatGPTCloudProgress{adapter: a, path: path, ctx: ctx, cancel: cancel, sessions: map[string]*chatGPTCloudProgressSession{}, confirm: confirm}, nil
}

func (p *chatGPTCloudProgress) ensure(id string, generation int64, persistent bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	if p.store == nil {
		store, err := newChatGPTCloudProgressStore(p.path)
		if err != nil {
			return err
		}
		p.store = store
	}
	if s := p.sessions[id]; s != nil {
		if generation == 0 || s.generation == generation {
			s.touched = time.Now()
			s.persistent = s.persistent || persistent
			return nil
		}
		s.cancel()
		delete(p.sessions, id)
	}
	if len(p.sessions) >= 64 {
		return fmt.Errorf("SSE progress subscription capacity reached")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	s := &chatGPTCloudProgressSession{cancel: cancel, generation: generation, persistent: persistent, touched: time.Now(), state: "connecting"}
	p.sessions[id] = s
	p.wg.Add(1)
	go p.run(ctx, id, s)
	return nil
}
func (p *chatGPTCloudProgress) release(id string, generation int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[id]; s != nil && (generation == 0 || s.generation == generation) {
		s.cancel()
		delete(p.sessions, id)
	}
}
func (p *chatGPTCloudProgress) Close() error {
	p.mu.Lock()
	p.cancel()
	p.mu.Unlock()
	p.wg.Wait()
	if p.store == nil {
		return nil
	}
	return p.store.Close()
}

func (p *chatGPTCloudProgress) recent(id string, generation int64) (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[id]; s != nil && s.generation == generation && s.state == "connected" && time.Since(s.lastProgress) < time.Minute {
		return s.progressKey, s.lastProgress
	}
	return "", time.Time{}
}

func (p *chatGPTCloudProgress) snapshot(ctx context.Context, id string, cursor int64, wait time.Duration) (map[string]any, error) {
	if err := p.ensure(id, 0, false); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, next, err := p.store.Snapshot(id, cursor)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		s := p.sessions[id]
		state := "stopped"
		var retry time.Time
		status := 0
		if s != nil {
			state = s.state
			retry = s.nextRetry
			status = s.httpStatus
		}
		p.mu.Unlock()
		out := map[string]any{"sessionId": id, "source": "sse_local", "events": events, "cursor": next, "connectionState": state, "authoritative": false, "note": "Local incremental progress only; session.get retains authoritative conversation reading and formal callbacks remain unchanged"}
		if status != 0 {
			out["httpStatus"] = status
		}
		if !retry.IsZero() {
			out["nextRetryAt"] = retry.UTC().Format(time.RFC3339)
		}
		if len(events) > 0 || wait == 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return out, nil
		case <-ticker.C:
		}
	}
}

func (p *chatGPTCloudProgress) run(ctx context.Context, id string, s *chatGPTCloudProgressSession) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		if p.sessions[id] == s {
			delete(p.sessions, id)
		}
		p.mu.Unlock()
	}()
	delay := 5 * time.Second
	for {
		p.mu.Lock()
		expired := !s.persistent && time.Since(s.touched) > 2*time.Minute
		s.state = "connecting"
		s.nextRetry = time.Time{}
		p.mu.Unlock()
		if expired || ctx.Err() != nil {
			return
		}
		// A bounded stream also bounds idle watchers and shutdown. Successful data
		// does not imply that the provider turn or business task has completed.
		p.mu.Lock()
		streamLimit := 2 * time.Minute
		if s.persistent {
			streamLimit = 30 * time.Minute
		}
		p.mu.Unlock()
		streamCtx, cancel := context.WithTimeout(ctx, streamLimit)
		code, err := p.connect(streamCtx, id, s)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if code == 404 {
			delay = 2 * time.Minute
		} else if code == 401 || code == 403 {
			delay = 5 * time.Minute
		} else if delay < 2*time.Minute {
			delay *= 2
		}
		if delay > 2*time.Minute && code != 401 && code != 403 {
			delay = 2 * time.Minute
		}
		if code == 429 {
			p.adapter.readBudget.mu.Lock()
			cooldown := time.Until(p.adapter.readBudget.cooldownUntil)
			p.adapter.readBudget.mu.Unlock()
			if cooldown > delay {
				delay = cooldown
			}
		}
		p.mu.Lock()
		s.httpStatus = code
		s.state = "reconnecting"
		if code == 404 {
			s.state = "unavailable"
		}
		if err == nil {
			s.state = "stream_ended"
		}
		s.nextRetry = time.Now().Add(delay)
		p.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *chatGPTCloudProgress) connect(ctx context.Context, id string, s *chatGPTCloudProgressSession) (int, error) {
	a := p.adapter
	if err := a.readBudget.acquire(ctx); err != nil {
		return 429, err
	}
	token, err := a.token(ctx)
	if err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"conversation_id": id, "offset": 0})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/backend-api/f/conversation/resume", strings.NewReader(string(raw)))
	if err != nil {
		return 0, err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if conduit := a.takeConduit(id); conduit != "" {
		req.Header.Set("x-conduit-token", conduit)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == 429 {
			a.readBudget.noteRateLimit(resp.Header.Get("Retry-After"))
		}
		return resp.StatusCode, fmt.Errorf("resume stream HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return 200, fmt.Errorf("resume response is not SSE")
	}
	p.mu.Lock()
	s.state = "connected"
	s.httpStatus = 200
	p.mu.Unlock()
	ordinal := 0
	err = readChatGPTProgressFrames(resp.Body, func(event, data string) error {
		ordinal++
		var body map[string]any
		_ = json.Unmarshal([]byte(data), &body)
		if body["type"] == "resume_conversation_token" {
			return nil
		}
		if cid, ok := body["conversation_id"].(string); ok && cid != "" && cid != id {
			return fmt.Errorf("resume conversation identity mismatch")
		}
		key := fmt.Sprintf("%d:%d:%x", s.generation, ordinal, sha256.Sum256([]byte(event+"\n"+data)))
		p.mu.Lock()
		current := p.sessions[id] == s
		p.mu.Unlock()
		if !current {
			return context.Canceled
		}
		added, err := p.store.AppendUnique(id, key, event, data)
		if err != nil {
			return err
		}
		if added && event == "delta" {
			p.mu.Lock()
			s.lastProgress = time.Now()
			s.progressKey = key
			p.mu.Unlock()
		}
		// Only explicit terminal hints may request the existing generation-fenced
		// detail confirmation. EOF and handoff deliberately have no such effect.
		if added && (body["type"] == "message_stream_complete" || body["type"] == "complete") && s.generation > 0 && p.confirm != nil {
			p.confirm(id, s.generation)
		}
		return nil
	})
	return resp.StatusCode, err
}

// SSE framing supports multiline data and bounded frames; dispatch only at a
// blank line. An incomplete frame at EOF is never persisted as a full event.
func readChatGPTProgressFrames(reader io.Reader, consume func(string, string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	event := "message"
	var data []string
	size := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				if err := consume(event, strings.Join(data, "\n")); err != nil {
					return err
				}
			}
			event = "message"
			data = nil
			size = 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			size += len(value)
			if size > 256*1024 {
				return fmt.Errorf("SSE frame exceeds progress cache limit")
			}
			data = append(data, value)
		}
	}
	return scanner.Err()
}
