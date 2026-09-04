package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// chatgptCloudEvent is a normalized realtime event delivered to session.watch for
// a chatgpt_cloud conversation. It mirrors the pubsub conversation-turn-complete /
// conversation-created / conversation-update(s) signals (which tell a client to
// refetch the conversation for new content).
type chatgptCloudEvent struct {
	Sequence          int64
	EventKey          string // stable provider identity, or a short-window fallback key; never a local cursor
	Type              string // normalized: conversation.turn.complete / conversation.created / conversation.updated
	ConversationID    string
	EventType         string // raw pubsub payload type
	Timestamp         time.Time
	ResultID          string
	ResultStatus      string
	ResultBytes       int64
	ResultSHA256      string
	ResultPageCount   int
	CallbackType      string
	ResultText        string
	CallbackOutcome   string
	CallbackErrorCode string
	DeliverablePath   string
	DeliverableStatus string
}

const chatgptRealtimeFallbackDedupWindow = 15 * time.Second

type chatgptCloudRealtime struct {
	logger      *slog.Logger
	baseURL     string
	http        *http.Client
	tokenSource func(ctx context.Context) (string, error)

	mu          sync.Mutex
	events      []chatgptCloudEvent
	nextEvent   int64
	seenKeys    map[string]struct{}
	seenOrder   []string
	notify      chan struct{}
	stateNotify chan struct{}
	subNotify   chan struct{}
	watching    map[string]*realtimeSubscription
	observer    func(chatgptCloudEvent)
	connected   bool
	disconnects uint64
	closed      bool
	rootCtx     context.Context
	cancel      context.CancelFunc
	startOnce   sync.Once
	closeOnce   sync.Once
	wg          sync.WaitGroup
}

const (
	maxChatGPTCloudRealtimeSubscriptions = 64
	chatgptRealtimeReconnectMin          = 5 * time.Second
	chatgptRealtimeReconnectMax          = 5 * time.Minute
	chatgptRealtimeHealthyConnection     = 30 * time.Second
)

type realtimeSubscription struct {
	conversationID string
	generation     int64
	lastUsed       time.Time
	waiters        int
	persistent     bool
}

func newChatGPTCloudRealtime(logger *slog.Logger, baseURL string, httpClient *http.Client, tokenSource func(ctx context.Context) (string, error)) *chatgptCloudRealtime {
	if logger == nil {
		logger = slog.Default()
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	return &chatgptCloudRealtime{
		logger:      logger,
		baseURL:     baseURL,
		http:        httpClient,
		tokenSource: tokenSource,
		notify:      make(chan struct{}),
		stateNotify: make(chan struct{}),
		subNotify:   make(chan struct{}, 1),
		watching:    map[string]*realtimeSubscription{},
		seenKeys:    map[string]struct{}{},
		rootCtx:     rootCtx,
		cancel:      cancel,
	}
}

func (r *chatgptCloudRealtime) emit(conversationID, eventType string, eventKey ...string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	key := ""
	if len(eventKey) > 0 {
		key = eventKey[0]
	}
	if key == "" {
		key = chatgptRealtimeEventKey(conversationID, eventType, nil)
	}
	if _, exists := r.seenKeys[key]; exists {
		r.mu.Unlock()
		return
	}
	r.seenKeys[key] = struct{}{}
	r.seenOrder = append(r.seenOrder, key)
	if len(r.seenOrder) > 1024 {
		oldest := r.seenOrder[0]
		r.seenOrder = r.seenOrder[1:]
		delete(r.seenKeys, oldest)
	}
	r.nextEvent++
	event := chatgptCloudEvent{
		Sequence:       r.nextEvent,
		EventKey:       key,
		ConversationID: conversationID,
		EventType:      eventType,
		Timestamp:      time.Now().UTC(),
	}
	switch eventType {
	case "conversation-turn-complete", "conversation-turn-completed":
		event.Type = "conversation.turn.complete"
	case "conversation-created":
		event.Type = "conversation.created"
	default:
		event.Type = "conversation.updated"
	}
	r.events = append(r.events, event)
	if len(r.events) > 256 {
		r.events = append([]chatgptCloudEvent(nil), r.events[len(r.events)-256:]...)
	}
	close(r.notify)
	r.notify = make(chan struct{})
	observer := r.observer
	r.mu.Unlock()
	if observer != nil {
		observer(event)
	}
}

func (r *chatgptCloudRealtime) setObserver(observer func(chatgptCloudEvent)) {
	r.mu.Lock()
	r.observer = observer
	r.mu.Unlock()
}

func (r *chatgptCloudRealtime) setSequenceFloor(sequence int64) {
	r.mu.Lock()
	if sequence > r.nextEvent {
		r.nextEvent = sequence
	}
	r.mu.Unlock()
}

// ensureWatching starts a pubsub subscription for a conversation if not active.
func (r *chatgptCloudRealtime) ensureWatching(ctx context.Context, conversationID string, waiting ...bool) (*realtimeSubscription, error) {
	isWaiting := len(waiting) > 0 && waiting[0]
	return r.ensureWatchingForGeneration(ctx, conversationID, isWaiting, 0)
}

func (r *chatgptCloudRealtime) ensureWatchingForGeneration(_ context.Context, conversationID string, isWaiting bool, generation int64) (*realtimeSubscription, error) {
	if conversationID == "" {
		return nil, fmt.Errorf("conversationId is required")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("chatgpt_cloud realtime is closed")
	}
	if active := r.watching[conversationID]; active != nil {
		if generation > 0 && active.generation == 0 {
			active.generation = generation
		}
		if generation > 0 && active.generation != 0 && active.generation > generation {
			r.mu.Unlock()
			return nil, fmt.Errorf("chatgpt_cloud realtime watcher generation is newer than requested generation")
		}
		if generation > 0 && active.generation != 0 && active.generation < generation {
			active.generation = generation
		}
		active.lastUsed = time.Now()
		if isWaiting {
			active.waiters++
		}
		r.mu.Unlock()
		r.startConnectionLoop()
		return active, nil
	}
	if len(r.watching) >= maxChatGPTCloudRealtimeSubscriptions {
		var evicted *realtimeSubscription
		for _, candidate := range r.watching {
			if candidate.waiters > 0 || candidate.persistent {
				continue
			}
			if evicted == nil || candidate.lastUsed.Before(evicted.lastUsed) {
				evicted = candidate
			}
		}
		if evicted == nil {
			r.mu.Unlock()
			return nil, fmt.Errorf("chatgpt_cloud realtime subscription limit reached (%d): all subscriptions have active waiters", maxChatGPTCloudRealtimeSubscriptions)
		}
		delete(r.watching, evicted.conversationID)
	}
	sub := &realtimeSubscription{conversationID: conversationID, generation: generation, lastUsed: time.Now()}
	if isWaiting {
		sub.waiters = 1
	}
	r.watching[conversationID] = sub
	r.mu.Unlock()
	r.signalSubscriptionChange()
	r.startConnectionLoop()
	return sub, nil
}

func (r *chatgptCloudRealtime) ensurePersistentWatching(ctx context.Context, conversationID string) error {
	return r.ensurePersistentWatchingForGeneration(ctx, conversationID, 0)
}

func (r *chatgptCloudRealtime) ensurePersistentWatchingForGeneration(ctx context.Context, conversationID string, generation int64) error {
	sub, err := r.ensureWatchingForGeneration(ctx, conversationID, false, generation)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if current := r.watching[conversationID]; current == sub {
		current.persistent = true
		current.lastUsed = time.Now()
	}
	r.mu.Unlock()
	return nil
}

func (r *chatgptCloudRealtime) releasePersistentWatching(conversationID string, generation ...int64) {
	wantedGeneration := int64(0)
	if len(generation) > 0 {
		wantedGeneration = generation[0]
	}
	r.mu.Lock()
	sub := r.watching[conversationID]
	if sub == nil {
		r.mu.Unlock()
		return
	}
	if wantedGeneration > 0 && sub.generation != wantedGeneration {
		r.mu.Unlock()
		return
	}
	sub.persistent = false
	if sub.waiters > 0 {
		sub.lastUsed = time.Now()
		r.mu.Unlock()
		return
	}
	delete(r.watching, conversationID)
	r.mu.Unlock()
	r.signalSubscriptionChange()
}

func (r *chatgptCloudRealtime) releaseWaiter(sub *realtimeSubscription) {
	r.mu.Lock()
	if sub != nil && r.watching[sub.conversationID] == sub && sub.waiters > 0 {
		sub.waiters--
		sub.lastUsed = time.Now()
	}
	r.mu.Unlock()
}

func (r *chatgptCloudRealtime) stopWatching(conversationID string) {
	r.mu.Lock()
	_, existed := r.watching[conversationID]
	delete(r.watching, conversationID)
	r.mu.Unlock()
	if existed {
		r.signalSubscriptionChange()
	}
}

func (r *chatgptCloudRealtime) signalSubscriptionChange() {
	if r == nil {
		return
	}
	select {
	case r.subNotify <- struct{}{}:
	default:
	}
}

// Close stops every active pubsub subscription and waits for its websocket
// connection (if any) to observe cancellation. It is safe to call repeatedly.
func (r *chatgptCloudRealtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.connected = false
		r.cancel()
		r.watching = map[string]*realtimeSubscription{}
		close(r.stateNotify)
		r.stateNotify = make(chan struct{})
		r.mu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *chatgptCloudRealtime) startConnectionLoop() {
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.runConnectionLoop()
		}()
	})
}

// runConnectionLoop maintains one account-level Celsius WebSocket, matching
// the official web client's connection model. All watched conversations share
// this socket instead of creating one provider connection per CHAT.
func (r *chatgptCloudRealtime) runConnectionLoop() {
	backoff := chatgptRealtimeReconnectMin
	for {
		started := time.Now()
		if err := r.runOnce(r.rootCtx); err != nil && r.rootCtx.Err() == nil {
			r.logger.Debug("chatgpt_cloud shared pubsub connection ended", "error", err, "retryAfter", backoff)
		}
		if r.rootCtx.Err() != nil {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-r.rootCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if time.Since(started) >= chatgptRealtimeHealthyConnection {
			backoff = chatgptRealtimeReconnectMin
		} else if backoff < chatgptRealtimeReconnectMax {
			backoff *= 2
			if backoff > chatgptRealtimeReconnectMax {
				backoff = chatgptRealtimeReconnectMax
			}
		}
	}
}

func (r *chatgptCloudRealtime) runOnce(ctx context.Context) error {
	if r.tokenSource == nil {
		return fmt.Errorf("chatgpt_cloud realtime token source is unavailable")
	}
	token, err := r.tokenSource(ctx)
	if err != nil {
		return err
	}
	wsURL, err := r.conversationWebSocketURL(ctx, token)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: chatgptCloudHeadersFromToken(token)})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Connect and subscribe in one batch, like the official client. A dedicated
	// writer below keeps this same socket synchronized when watched CHATs change.
	connect := []map[string]any{{"id": 1, "command": map[string]any{"type": "connect", "presence": map[string]any{"type": "presence", "state": "background"}}}}
	if err := writeWSFrame(ctx, conn, connect); err != nil {
		return err
	}
	initialIDs := r.watchedConversationIDs()
	subscribe := []map[string]any{{"id": 2, "command": map[string]any{"type": "subscribe", "topic_id": "conversations"}}}
	for index, conversationID := range initialIDs {
		subscribe = append(subscribe, map[string]any{"id": index + 3, "command": map[string]any{"type": "subscribe", "topic_id": "conversation-" + conversationID}})
	}
	if err := writeWSFrame(ctx, conn, subscribe); err != nil {
		return err
	}
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	initial := make(map[string]struct{}, len(initialIDs))
	for _, conversationID := range initialIDs {
		initial[conversationID] = struct{}{}
	}
	go func() {
		writerDone <- r.runSubscriptionWriter(connectionCtx, cancelConnection, conn, initial, len(subscribe)+2)
	}()
	r.setConnected(true)
	defer r.setConnected(false)
	defer cancelConnection()

	for {
		_, data, err := conn.Read(connectionCtx)
		if err != nil {
			cancelConnection()
			writerErr := <-writerDone
			if writerErr != nil && ctx.Err() == nil {
				return writerErr
			}
			return err
		}
		chatgptHandleWSFramesWithKey(data, func(topic, payloadType, cid, eventKey string) {
			if cid != "" && r.isWatching(cid) && (topic == "conversations" || topic == "conversation-"+cid) {
				r.emit(cid, payloadType, eventKey)
			}
		})
	}
}

func (r *chatgptCloudRealtime) runSubscriptionWriter(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, subscribed map[string]struct{}, nextCommandID int) error {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.subNotify:
			currentIDs := r.watchedConversationIDs()
			current := make(map[string]struct{}, len(currentIDs))
			for _, conversationID := range currentIDs {
				current[conversationID] = struct{}{}
			}
			added := make([]string, 0)
			removed := make([]string, 0)
			for conversationID := range current {
				if _, ok := subscribed[conversationID]; !ok {
					added = append(added, conversationID)
				}
			}
			for conversationID := range subscribed {
				if _, ok := current[conversationID]; !ok {
					removed = append(removed, conversationID)
				}
			}
			sort.Strings(added)
			sort.Strings(removed)
			commands := make([]map[string]any, 0, len(added)+len(removed))
			for _, conversationID := range added {
				commands = append(commands, map[string]any{"id": nextCommandID, "command": map[string]any{"type": "subscribe", "topic_id": "conversation-" + conversationID}})
				nextCommandID++
			}
			for _, conversationID := range removed {
				commands = append(commands, map[string]any{"id": nextCommandID, "command": map[string]any{"type": "unsubscribe", "topic_id": "conversation-" + conversationID}})
				nextCommandID++
			}
			if len(commands) == 0 {
				continue
			}
			if err := writeWSFrame(ctx, conn, commands); err != nil {
				return err
			}
			for _, conversationID := range added {
				subscribed[conversationID] = struct{}{}
			}
			for _, conversationID := range removed {
				delete(subscribed, conversationID)
			}
		}
	}
}

func (r *chatgptCloudRealtime) watchedConversationIDs() []string {
	r.mu.Lock()
	ids := make([]string, 0, len(r.watching))
	for conversationID := range r.watching {
		ids = append(ids, conversationID)
	}
	r.mu.Unlock()
	sort.Strings(ids)
	return ids
}

func (r *chatgptCloudRealtime) isWatching(conversationID string) bool {
	r.mu.Lock()
	_, ok := r.watching[conversationID]
	r.mu.Unlock()
	return ok
}

func (r *chatgptCloudRealtime) setConnected(connected bool) {
	r.mu.Lock()
	if r.connected != connected {
		if r.connected && !connected {
			r.disconnects++
		}
		r.connected = connected
		close(r.stateNotify)
		r.stateNotify = make(chan struct{})
	}
	r.mu.Unlock()
}

func (r *chatgptCloudRealtime) recoveryState() (bool, uint64) {
	if r == nil {
		return false, 0
	}
	r.mu.Lock()
	connected, disconnects := r.connected, r.disconnects
	r.mu.Unlock()
	return connected, disconnects
}

func (r *chatgptCloudRealtime) waitUntilConnected(ctx context.Context) error {
	for {
		r.mu.Lock()
		if r.connected {
			r.mu.Unlock()
			return nil
		}
		if r.closed {
			r.mu.Unlock()
			return fmt.Errorf("chatgpt_cloud realtime is closed")
		}
		notify := r.stateNotify
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

func writeWSFrame(ctx context.Context, conn *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

func chatgptHandleWSFrames(data []byte, onMessage func(topic, payloadType, conversationID string)) {
	chatgptHandleWSFramesWithKey(data, func(topic, payloadType, conversationID, _ string) {
		onMessage(topic, payloadType, conversationID)
	})
}

func chatgptHandleWSFramesWithKey(data []byte, onMessage func(topic, payloadType, conversationID, eventKey string)) {
	var frames []map[string]any
	if err := json.Unmarshal(data, &frames); err != nil {
		var single map[string]any
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return
		}
		frames = []map[string]any{single}
	}
	for _, frame := range frames {
		kind, _ := frame["type"].(string)
		if kind != "message" {
			continue
		}
		topic, _ := frame["topic_id"].(string)
		payload, _ := frame["payload"].(map[string]any)
		if payload == nil {
			continue
		}
		// payload may be {type, payload:{conversation_id}} or {type, conversation_id}
		payloadType, _ := payload["type"].(string)
		cid, _ := payload["conversation_id"].(string)
		if cid == "" {
			if inner, ok := payload["payload"].(map[string]any); ok {
				cid, _ = inner["conversation_id"].(string)
			}
		}
		onMessage(topic, payloadType, cid, chatgptRealtimeEventKey(cid, payloadType, payload))
	}
}

// chatgptRealtimeEventKey is a bounded fingerprint of the provider payload.
// The provider payload is the only source of identity here; the local sequence
// is deliberately excluded so reconnects and dual-topic delivery compare equal.
func chatgptRealtimeEventKey(conversationID, eventType string, payload map[string]any) string {
	return chatgptRealtimeEventKeyAt(conversationID, eventType, payload, time.Now().UTC())
}

func chatgptRealtimeEventKeyAt(conversationID, eventType string, payload map[string]any, now time.Time) string {
	stableID := chatgptRealtimePayloadStableID(payload)
	if stableID != "" {
		raw, _ := json.Marshal(struct {
			ConversationID string `json:"conversationId"`
			EventType      string `json:"eventType"`
			StableID       string `json:"stableId"`
		}{conversationID, eventType, stableID})
		hash := sha256.Sum256(raw)
		return "provider_evt_" + hex.EncodeToString(hash[:])[:48]
	}
	raw, _ := json.Marshal(struct {
		ConversationID string         `json:"conversationId"`
		EventType      string         `json:"eventType"`
		Payload        map[string]any `json:"payload,omitempty"`
	}{conversationID, eventType, payload})
	hash := sha256.Sum256(raw)
	// No provider identity was present. The bucket is intentionally short-lived:
	// it suppresses duplicate topic/reconnect delivery without treating an
	// identical payload as a permanent event identity.
	bucket := now.UnixNano() / chatgptRealtimeFallbackDedupWindow.Nanoseconds()
	return fmt.Sprintf("fallback_evt_%d_%s", bucket, hex.EncodeToString(hash[:])[:32])
}

// chatgptRealtimePayloadStableID only consumes identity fields that are
// actually present in the provider payload. If none is present, callers use
// the complete payload fingerprint above instead of inventing an identifier.
func chatgptRealtimePayloadStableID(payload map[string]any) string {
	for _, key := range []string{"event_id", "eventId", "turn_id", "turnId", "turn_exchange_id", "turnExchangeId", "message_id", "messageId"} {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	for _, key := range []string{"payload", "data", "metadata"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if value := chatgptRealtimePayloadStableID(nested); value != "" {
				return value
			}
		}
	}
	return ""
}

func (r *chatgptCloudRealtime) conversationWebSocketURL(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/backend-api/celsius/ws/user", nil)
	if err != nil {
		return "", err
	}
	chatgptApplyCloudHeaders(req, token)
	resp, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("celsius ws/user returned %s", resp.Status)
	}
	var out struct {
		WebsocketURL string `json:"websocket_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.WebsocketURL == "" {
		return "", fmt.Errorf("celsius ws/user returned no websocket url")
	}
	return out.WebsocketURL, nil
}

// watch returns realtime events for a conversation after the given cursor,
// long-polling up to wait. Returns (events, nextCursor, error).
func (r *chatgptCloudRealtime) watch(ctx context.Context, conversationID string, cursor int64, wait time.Duration) ([]chatgptCloudEvent, int64, error) {
	if wait < 0 || wait > 15*time.Second {
		return nil, cursor, fmt.Errorf("wait is outside the allowed range")
	}
	isWaiting := wait > 0
	sub, err := r.ensureWatching(ctx, conversationID, isWaiting)
	if err != nil {
		return nil, cursor, err
	}
	if isWaiting {
		defer r.releaseWaiter(sub)
	}
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		var events []chatgptCloudEvent
		for _, event := range r.events {
			if event.Sequence > cursor && event.ConversationID == conversationID {
				events = append(events, event)
				if len(events) >= 100 {
					break
				}
			}
		}
		next := cursor
		if len(events) > 0 {
			next = events[len(events)-1].Sequence
		}
		notify := r.notify
		r.mu.Unlock()
		if len(events) > 0 || wait == 0 || !time.Now().Before(deadline) {
			return events, next, nil
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, cursor, ctx.Err()
		case <-notify:
			timer.Stop()
		case <-timer.C:
			return nil, cursor, nil
		}
	}
}

// chatgptCloudHeadersFromToken returns a plain header map for the websocket dial.
func chatgptCloudHeadersFromToken(token string) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", chatgptCloudUA)
	headers.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	headers.Set("OAI-Language", "en")
	headers.Set("oai-did", chatgptCloudDeviceID())
	headers.Set("Origin", "https://chatgpt.com")
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	return headers
}
