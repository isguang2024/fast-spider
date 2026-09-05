package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const (
	chatgptCloudBaseURL         = "https://chatgpt.com"
	chatgptConversationPath     = "/backend-api/f/conversation"
	chatgptConversationPrepare  = "/backend-api/f/conversation/prepare"
	chatgptConversationDetail   = "/backend-api/conversation/{id}"
	chatgptConversationsList    = "/backend-api/conversations"
	chatgptSteerTurnPath        = "/backend-api/f/steer_turn"
	chatgptStopPath             = "/backend-api/stop_conversation"
	chatgptStreamTimeout        = 120 * time.Second
	chatgptListAuthTimeout      = 6 * time.Second
	chatgptListRequestTimeout   = 8 * time.Second
	chatgptReconcileListLimit   = 12
	chatgptReconcileReadTimeout = 2 * time.Second
	chatgptTokenCacheTTL        = 30 * time.Second
)

// ChatGPTCloudAdapter drives ChatGPT cloud conversations through the same
// /backend-api/f/conversation flow the official client uses, authenticating with
// the Codex app-server's ChatGPT token and solving the Sentinel challenge itself.
type ChatGPTCloudAdapter struct {
	logger             *slog.Logger
	baseURL            string
	http               *http.Client
	tokenSource        func(ctx context.Context) (string, error)
	tokenMu            sync.Mutex
	cachedToken        string
	cachedTokenUntil   time.Time
	realtime           *chatgptCloudRealtime
	listAuthTimeout    time.Duration
	listRequestTimeout time.Duration

	readBudgetMu sync.Mutex
	readBudget   *chatGPTCloudProviderReadBudget

	conduitMu             sync.Mutex
	conduitByConversation map[string]string
	createOverride        func(context.Context, string, string) (chatgptCloudTurnResult, error)
	sendOverride          func(context.Context, string, string, string, string, string) (chatgptCloudTurnResult, error)
}

type chatgptCloudTurnResult struct {
	ConversationID string
	Messages       []chatgptCloudMessage
	AsyncTaskID    string
	TurnExchangeID string
	ChimeVersion   any
	AsyncStatus    any
	Model          string
	Thinking       string
	Replayed       bool
}

type chatgptCloudMessage struct {
	ID     string
	Role   string
	Text   string
	Status string
}

func NewChatGPTCloudAdapter(logger *slog.Logger, tokenSource func(ctx context.Context) (string, error)) *ChatGPTCloudAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	adapter := &ChatGPTCloudAdapter{
		logger:                logger,
		baseURL:               chatgptCloudBaseURL,
		http:                  newChatGPTCloudHTTPClient(),
		tokenSource:           tokenSource,
		listAuthTimeout:       chatgptListAuthTimeout,
		listRequestTimeout:    chatgptListRequestTimeout,
		readBudget:            newChatGPTCloudProviderReadBudget(logger),
		conduitByConversation: map[string]string{},
	}
	adapter.realtime = newChatGPTCloudRealtime(logger, adapter.baseURL, adapter.http, tokenSource)
	return adapter
}

// WatchRealtime returns live pubsub events for a conversation after the cursor.
func (a *ChatGPTCloudAdapter) WatchRealtime(ctx context.Context, conversationID string, cursor int64, wait time.Duration) ([]chatgptCloudEvent, int64, error) {
	return a.realtime.watch(ctx, conversationID, cursor, wait)
}

func (a *ChatGPTCloudAdapter) SetRealtimeObserver(observer func(chatgptCloudEvent), sequenceFloor int64) {
	if a == nil || a.realtime == nil {
		return
	}
	a.realtime.setSequenceFloor(sequenceFloor)
	a.realtime.setObserver(observer)
}

func (a *ChatGPTCloudAdapter) EnsureCallbackRealtime(ctx context.Context, conversationID string) error {
	return a.EnsureCallbackRealtimeForGeneration(ctx, conversationID, 0)
}

func (a *ChatGPTCloudAdapter) EnsureCallbackRealtimeForGeneration(ctx context.Context, conversationID string, generation int64) error {
	if a == nil || a.realtime == nil {
		return fmt.Errorf("chatgpt_cloud realtime is unavailable")
	}
	return a.realtime.ensurePersistentWatchingForGeneration(ctx, conversationID, generation)
}

func (a *ChatGPTCloudAdapter) WaitCallbackRealtime(ctx context.Context) error {
	if a == nil || a.realtime == nil {
		return fmt.Errorf("chatgpt_cloud realtime is unavailable")
	}
	return a.realtime.waitUntilConnected(ctx)
}

func (a *ChatGPTCloudAdapter) IsWatchingRealtime(conversationID string) bool {
	return a != nil && a.realtime != nil && a.realtime.isWatching(conversationID)
}

func (a *ChatGPTCloudAdapter) CallbackRealtimeRecoveryState() (bool, uint64) {
	if a == nil || a.realtime == nil {
		return false, 0
	}
	return a.realtime.recoveryState()
}

func (a *ChatGPTCloudAdapter) ReleaseCallbackRealtime(conversationID string) {
	a.ReleaseCallbackRealtimeForGeneration(conversationID, 0)
}

func (a *ChatGPTCloudAdapter) ReleaseCallbackRealtimeForGeneration(conversationID string, generation int64) {
	if a != nil && a.realtime != nil {
		a.realtime.releasePersistentWatching(conversationID, generation)
	}
}

// StopRealtime terminates the pubsub subscription for a conversation.
func (a *ChatGPTCloudAdapter) StopRealtime(conversationID string) {
	if a != nil && a.realtime != nil {
		a.realtime.stopWatching(conversationID)
	}
}

// Close terminates realtime subscriptions and releases idle HTTP connections.
func (a *ChatGPTCloudAdapter) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var firstErr error
	if a.realtime != nil {
		if err := a.realtime.Close(ctx); err != nil {
			firstErr = err
		}
	}
	if a.http != nil {
		a.http.CloseIdleConnections()
	}
	return firstErr
}

func (a *ChatGPTCloudAdapter) cacheConduit(conversationID, conduit string) {
	if conversationID == "" || conduit == "" {
		return
	}
	a.conduitMu.Lock()
	a.conduitByConversation[conversationID] = conduit
	a.conduitMu.Unlock()
}

func (a *ChatGPTCloudAdapter) takeConduit(conversationID string) string {
	a.conduitMu.Lock()
	defer a.conduitMu.Unlock()
	return a.conduitByConversation[conversationID]
}

// newChatGPTCloudHTTPClient builds an HTTP client that presents a Chrome TLS
// fingerprint (utls) over HTTP/2. The ChatGPT anti-abuse edge flags Go's default
// TLS ClientHello and negotiates h2 regardless of client ALPN, so the transport
// must be h2-aware.
func newChatGPTCloudHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	utlsDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		uconn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_133)
		if err := uconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return uconn, nil
	}
	tr := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return utlsDial(ctx, network, addr)
		},
	}
	// Do not impose a client-wide timeout shorter than the bounded SSE stream.
	// Dial and per-operation contexts provide the limits; a global 60s timeout
	// previously turned successful cloud creates into ambiguous failures.
	return &http.Client{Transport: tr}
}

// token resolves the desktop-app ChatGPT access token.
func (a *ChatGPTCloudAdapter) token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()
	if a.cachedToken != "" && time.Now().Before(a.cachedTokenUntil) {
		return a.cachedToken, nil
	}
	if a.tokenSource == nil {
		return "", fmt.Errorf("chatgpt_cloud token source is unavailable")
	}
	token, err := a.tokenSource(ctx)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("chatgpt_cloud requires a ChatGPT-authenticated Codex app-server")
	}
	a.cachedToken = token
	a.cachedTokenUntil = time.Now().Add(chatgptTokenCacheTTL)
	return token, nil
}

func chatgptUserMessage(text string) map[string]any {
	return chatgptUserMessageWithID(text, "")
}

func chatgptUserMessageWithID(text, messageID string) map[string]any {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		messageID = chatgptCloudUUID()
	}
	return map[string]any{
		"id":          messageID,
		"author":      map[string]any{"role": "user"},
		"create_time": float64(time.Now().UnixMilli()) / 1000,
		"content":     map[string]any{"content_type": "text", "parts": []string{text}},
		"metadata":    map[string]any{"selected_sources": []string{}, "serialization_metadata": map[string]any{"custom_symbol_offsets": []string{}}},
	}
}

func chatgptNewChatBody(prompt, model string) map[string]any {
	return chatgptNewChatBodyWithThinking(prompt, model, "")
}

func chatgptNewChatBodyWithThinking(prompt, model, thinking string) map[string]any {
	body := chatgptConversationBody(prompt, model, "", "", "success")
	chatgptApplyThinkingEffort(body, thinking)
	return body
}

func chatgptCloudRequestMessageID(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		return ""
	}
	message, _ := messages[0].(map[string]any)
	return strings.TrimSpace(mapString(message, "id"))
}

func chatgptCloudSetRequestMessageID(body map[string]any, requestMessageID string) error {
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		return fmt.Errorf("could not construct idempotent ChatGPT follow-up")
	}
	message, _ := messages[0].(map[string]any)
	if message == nil {
		return fmt.Errorf("could not construct idempotent ChatGPT follow-up")
	}
	message["id"] = requestMessageID
	return nil
}

// chatgptQuickChatBody mirrors the Codex Quick chat composer: it does not use
// /f/conversation/prepare, reports client_prepare_state=none, and lets ChatGPT
// choose the model through the "auto" route unless the caller selected one.
func chatgptQuickChatBody(prompt, model string) map[string]any {
	return chatgptQuickChatBodyWithThinking(prompt, model, "")
}

func chatgptQuickChatBodyWithThinking(prompt, model, thinking string) map[string]any {
	body := map[string]any{
		"action":               "next",
		"messages":             []any{chatgptUserMessage(prompt)},
		"model":                firstNonEmptyString(model, "auto"),
		"parent_message_id":    chatgptCloudUUID(),
		"client_prepare_state": "none",
		"supported_encodings":  []string{"v1"},
		"timezone_offset_min":  -480,
		"timezone":             "Etc/GMT-8",
	}
	chatgptApplyThinkingEffort(body, thinking)
	return body
}

func chatgptApplyThinkingEffort(body map[string]any, thinking string) {
	if thinking = strings.TrimSpace(thinking); thinking != "" {
		body["thinking_effort"] = thinking
	}
}

func chatgptFollowUpBody(conversationID, parentMessageID, prompt, model string) map[string]any {
	return chatgptFollowUpBodyWithThinking(conversationID, parentMessageID, prompt, model, "")
}

func chatgptFollowUpBodyWithThinking(conversationID, parentMessageID, prompt, model, thinking string) map[string]any {
	body := chatgptConversationBody(prompt, model, conversationID, parentMessageID, "sent")
	chatgptApplyThinkingEffort(body, thinking)
	return body
}

func chatgptConversationBody(prompt, model, conversationID, parentMessageID, prepareState string) map[string]any {
	body := map[string]any{
		"action":                               "next",
		"messages":                             []any{chatgptUserMessage(prompt)},
		"model":                                firstNonEmptyString(model, "gpt-5-6"),
		"client_prepare_state":                 prepareState,
		"timezone_offset_min":                  -480,
		"timezone":                             "Etc/GMT-8",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []any{},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"client_contextual_info":               map[string]any{"app_name": "fast_spider"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
		"local_function_names":                 []string{},
	}
	if conversationID != "" {
		body["conversation_id"] = conversationID
		body["parent_message_id"] = parentMessageID
	} else {
		body["parent_message_id"] = "client-created-root"
	}
	return body
}

// sendTurn runs one full /f turn: solve sentinel, prepare (conduit), stream.
func (a *ChatGPTCloudAdapter) sendTurn(ctx context.Context, body map[string]any) (chatgptCloudTurnResult, error) {
	return a.sendTurnToObserved(ctx, chatgptConversationPath, body, nil)
}

func (a *ChatGPTCloudAdapter) sendTurnTo(ctx context.Context, path string, body map[string]any) (chatgptCloudTurnResult, error) {
	return a.sendTurnToObserved(ctx, path, body, nil)
}

func (a *ChatGPTCloudAdapter) sendTurnToObserved(ctx context.Context, path string, body map[string]any, onConversationID func(chatgptCloudTurnResult) error) (chatgptCloudTurnResult, error) {
	token, err := a.token(ctx)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	sentinel, err := chatgptSentinelHeaders(ctx, a.http, a.baseURL, token)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	conduit, err := a.prepare(ctx, token, body)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	result, err := a.streamPathObserved(ctx, path, token, conduit, body, sentinel, onConversationID)
	if result.ConversationID != "" {
		a.cacheConduit(result.ConversationID, conduit)
	}
	// Preserve a provider-emitted conversation ID even if the tail of the SSE
	// stream later fails. The manager can then persist the created conversation
	// instead of marking the entire create as unknown and inviting a duplicate.
	return result, err
}

func (a *ChatGPTCloudAdapter) prepare(ctx context.Context, token string, body map[string]any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+chatgptConversationPrepare, strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", a.providerHTTPError("conversation prepare", resp)
	}
	var out struct {
		Status       string `json:"status"`
		ConduitToken string `json:"conduit_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ConduitToken, nil
}

func (a *ChatGPTCloudAdapter) stream(ctx context.Context, token, conduit string, body map[string]any, sentinel map[string]string) (chatgptCloudTurnResult, error) {
	return a.streamPathObserved(ctx, chatgptConversationPath, token, conduit, body, sentinel, nil)
}

func (a *ChatGPTCloudAdapter) streamPath(ctx context.Context, path, token, conduit string, body map[string]any, sentinel map[string]string) (chatgptCloudTurnResult, error) {
	return a.streamPathObserved(ctx, path, token, conduit, body, sentinel, nil)
}

func (a *ChatGPTCloudAdapter) streamPathObserved(ctx context.Context, path, token, conduit string, body map[string]any, sentinel map[string]string, onConversationID func(chatgptCloudTurnResult) error) (chatgptCloudTurnResult, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, strings.NewReader(string(raw)))
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	if conduit != "" {
		req.Header.Set("x-conduit-token", conduit)
	}
	for key, value := range sentinel {
		req.Header.Set(key, value)
	}

	streamCtx, cancel := context.WithTimeout(ctx, chatgptStreamTimeout)
	defer cancel()
	resp, err := a.http.Do(req.WithContext(streamCtx))
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		label := "conversation stream"
		if path == chatgptSteerTurnPath {
			label = "steer turn"
		}
		return chatgptCloudTurnResult{}, a.providerHTTPError(label, resp)
	}
	return chatgptParseStreamObserved(resp.Body, 20000, onConversationID)
}

type chatgptCloudTurnOutcome struct {
	Result chatgptCloudTurnResult
	Err    error
}

// streamQuick starts the same unprepared /f/conversation stream used by Codex
// Quick chat. It returns as soon as ChatGPT emits the real conversation ID, then
// keeps draining and closing the SSE response in the background.
func (a *ChatGPTCloudAdapter) streamQuick(ctx context.Context, token string, body map[string]any, sentinel map[string]string) (chatgptCloudTurnResult, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}

	streamCtx, cancelStream := context.WithTimeout(context.WithoutCancel(ctx), chatgptStreamTimeout)
	stopCallerCancellation := make(chan struct{})
	defer close(stopCallerCancellation)
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancelStream()
			case <-stopCallerCancellation:
			case <-streamCtx.Done():
			}
		}()
	}

	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, a.baseURL+chatgptConversationPath, strings.NewReader(string(raw)))
	if err != nil {
		cancelStream()
		return chatgptCloudTurnResult{}, err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range sentinel {
		req.Header.Set(key, value)
	}

	resp, err := a.http.Do(req)
	if err != nil {
		cancelStream()
		return chatgptCloudTurnResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		defer cancelStream()
		return chatgptCloudTurnResult{}, a.providerHTTPError("conversation stream", resp)
	}

	firstResult := make(chan chatgptCloudTurnOutcome, 1)
	go func() {
		defer cancelStream()
		defer resp.Body.Close()
		announced := false
		result, parseErr := chatgptParseStreamObserved(resp.Body, 20000, func(observed chatgptCloudTurnResult) error {
			if announced {
				return nil
			}
			announced = true
			firstResult <- chatgptCloudTurnOutcome{Result: observed}
			return nil
		})
		if !announced {
			firstResult <- chatgptCloudTurnOutcome{Result: result, Err: parseErr}
			return
		}
		if parseErr != nil {
			a.logger.Warn("drain ChatGPT Quick chat stream", "conversationId", result.ConversationID, "error", parseErr)
		}
	}()

	select {
	case outcome := <-firstResult:
		return outcome.Result, outcome.Err
	case <-ctx.Done():
		cancelStream()
		return chatgptCloudTurnResult{}, ctx.Err()
	}
}

// chatgptParseStream parses the /f/conversation SSE stream: collects the
// conversation id and any inline message events, stopping at message_stream_complete.
func chatgptParseStream(reader io.Reader, maxEvents int) (chatgptCloudTurnResult, error) {
	return chatgptParseStreamObserved(reader, maxEvents, nil)
}

func chatgptParseStreamObserved(reader io.Reader, maxEvents int, onConversationID func(chatgptCloudTurnResult) error) (chatgptCloudTurnResult, error) {
	result := chatgptCloudTurnResult{}
	var observedErr error
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	var dataLines []string
	events := 0
	flush := func() bool {
		if len(dataLines) == 0 {
			return false
		}
		events++
		payload := strings.Join(dataLines, "\n")
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err == nil {
			observedConversationID := false
			if cid, _ := data["conversation_id"].(string); cid != "" && result.ConversationID == "" {
				result.ConversationID = cid
				observedConversationID = true
			}
			if status, ok := chatgptCloudFirstMapValue(data, "conversation_async_status", "async_status"); ok {
				result.AsyncStatus = status
			}
			if taskID := chatgptCloudAsyncTaskIDInValue(data); taskID != "" {
				result.AsyncTaskID = taskID
			}
			if msg, ok := data["message"].(map[string]any); ok {
				result.Messages = append(result.Messages, chatgptCloudMessageFromMap(msg))
				chatgptCloudCaptureTurnMetadata(&result, msg)
			}
			if input, ok := data["input_message"].(map[string]any); ok {
				result.Messages = append(result.Messages, chatgptCloudMessageFromMap(input))
				chatgptCloudCaptureTurnMetadata(&result, input)
			}
			if observedConversationID && onConversationID != nil {
				if err := onConversationID(result); err != nil {
					observedErr = err
					return true
				}
			}
			if data["type"] == "message_stream_complete" || data["type"] == "complete" {
				return true // complete: stop
			}
		}
		dataLines = nil
		return false
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(line[5:]))
		case line == "":
			done := flush()
			if done || events > maxEvents {
				return result, observedErr
			}
		}
	}
	if len(dataLines) > 0 {
		flush()
	}
	if observedErr != nil {
		return result, observedErr
	}
	return result, scanner.Err()
}

func chatgptCloudMessageFromMap(m map[string]any) chatgptCloudMessage {
	msg := chatgptCloudMessage{}
	msg.ID, _ = m["id"].(string)
	if author, ok := m["author"].(map[string]any); ok {
		msg.Role, _ = author["role"].(string)
	}
	msg.Status, _ = m["status"].(string)
	if content, ok := m["content"].(map[string]any); ok {
		if parts, ok := content["parts"].([]any); ok {
			var sb strings.Builder
			for _, p := range parts {
				sb.WriteString(fmt.Sprintf("%v", p))
			}
			msg.Text = sb.String()
		}
	}
	return msg
}

// Create starts a new cloud conversation with the first user message.
func (a *ChatGPTCloudAdapter) Create(ctx context.Context, prompt, model string) (chatgptCloudTurnResult, error) {
	return a.CreateWithThinking(ctx, prompt, model, "")
}

func (a *ChatGPTCloudAdapter) CreateWithThinking(ctx context.Context, prompt, model, thinking string) (chatgptCloudTurnResult, error) {
	return a.CreateWithThinkingObserved(ctx, prompt, model, thinking, nil)
}

func (a *ChatGPTCloudAdapter) CreateWithThinkingObserved(ctx context.Context, prompt, model, thinking string, onConversationID func(chatgptCloudTurnResult) error) (chatgptCloudTurnResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("creating a ChatGPT cloud conversation requires a first message")
	}
	if a.createOverride != nil {
		return a.createOverride(ctx, prompt, model)
	}
	return a.createBodyObserved(ctx, chatgptNewChatBodyWithThinking(prompt, model, thinking), onConversationID)
}

func (a *ChatGPTCloudAdapter) createBodyObserved(ctx context.Context, body map[string]any, onConversationID func(chatgptCloudTurnResult) error) (chatgptCloudTurnResult, error) {
	return a.sendTurnToObserved(ctx, chatgptConversationPath, body, onConversationID)
}

// CreateQuick starts a new cloud conversation with Codex Quick chat semantics:
// skip conversation prepare and return once the real conversation ID is known.
func (a *ChatGPTCloudAdapter) CreateQuick(ctx context.Context, prompt, model string) (chatgptCloudTurnResult, error) {
	return a.CreateQuickWithThinking(ctx, prompt, model, "")
}

func (a *ChatGPTCloudAdapter) CreateQuickWithThinking(ctx context.Context, prompt, model, thinking string) (chatgptCloudTurnResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("creating a ChatGPT cloud conversation requires a first message")
	}
	if a.createOverride != nil {
		return a.createOverride(ctx, prompt, firstNonEmptyString(model, "auto"))
	}
	return a.createQuickBody(ctx, chatgptQuickChatBodyWithThinking(prompt, model, thinking))
}

func (a *ChatGPTCloudAdapter) createQuickBody(ctx context.Context, body map[string]any) (chatgptCloudTurnResult, error) {
	token, err := a.token(ctx)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	sentinel, err := chatgptSentinelHeaders(ctx, a.http, a.baseURL, token)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	return a.streamQuick(ctx, token, body, sentinel)
}

// Send appends a follow-up message to an existing conversation. If parentMessageID
// is empty it is resolved to the latest assistant message (like the official client).
func (a *ChatGPTCloudAdapter) Send(ctx context.Context, conversationID, parentMessageID, prompt, model string) (chatgptCloudTurnResult, error) {
	return a.SendWithThinking(ctx, conversationID, parentMessageID, prompt, model, "")
}

// SendWithThinking appends a follow-up while preserving the model and reasoning
// effort selected for the conversation's first assistant turn unless explicitly
// overridden by the caller.
func (a *ChatGPTCloudAdapter) SendWithThinking(ctx context.Context, conversationID, parentMessageID, prompt, model, thinking string) (chatgptCloudTurnResult, error) {
	body, parentMessageID, model, thinking, err := a.followUpBody(ctx, conversationID, parentMessageID, prompt, model, thinking)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	var result chatgptCloudTurnResult
	a.InvalidateRead(conversationID)
	defer a.InvalidateRead(conversationID)
	if a.sendOverride != nil {
		result, err = a.sendOverride(ctx, conversationID, parentMessageID, prompt, model, thinking)
	} else {
		result, err = a.sendTurn(ctx, body)
	}
	result.Model = strings.TrimSpace(model)
	result.Thinking = strings.TrimSpace(thinking)
	return result, err
}

// SendQuickWithThinking appends a follow-up and returns as soon as ChatGPT has
// accepted the turn for the existing conversation. The response stream keeps
// draining in the background, matching Quick chat creation semantics.
func (a *ChatGPTCloudAdapter) SendQuickWithThinking(ctx context.Context, conversationID, parentMessageID, prompt, model, thinking string) (chatgptCloudTurnResult, error) {
	body, parentMessageID, model, thinking, err := a.followUpBody(ctx, conversationID, parentMessageID, prompt, model, thinking)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	var result chatgptCloudTurnResult
	a.InvalidateRead(conversationID)
	defer a.InvalidateRead(conversationID)
	if a.sendOverride != nil {
		result, err = a.sendOverride(ctx, conversationID, parentMessageID, prompt, model, thinking)
	} else {
		result, err = a.createQuickBody(ctx, body)
	}
	if result.ConversationID == "" {
		result.ConversationID = conversationID
	}
	result.Model = strings.TrimSpace(model)
	result.Thinking = strings.TrimSpace(thinking)
	return result, err
}

// SendQuickIdempotentWithThinking appends a follow-up using a caller-stable
// provider message ID. A retry first reconciles that exact ID in the selected
// conversation and therefore does not create a second turn after a process or
// transport interruption.
func (a *ChatGPTCloudAdapter) SendQuickIdempotentWithThinking(ctx context.Context, conversationID, parentMessageID, prompt, model, thinking, requestMessageID string) (chatgptCloudTurnResult, error) {
	conversationID = strings.TrimSpace(conversationID)
	prompt = strings.TrimSpace(prompt)
	requestMessageID = strings.TrimSpace(requestMessageID)
	if conversationID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("conversationId is required")
	}
	if prompt == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("message text is required")
	}
	if requestMessageID == "" || len(requestMessageID) > 128 || strings.ContainsAny(requestMessageID, "\x00\r\n\t ") {
		return chatgptCloudTurnResult{}, fmt.Errorf("request message id is required and must be at most 128 non-whitespace characters")
	}
	detail, err := a.ReadCached(withChatGPTCloudReadSource(ctx, "send_idempotency"), conversationID, chatgptCloudProviderReadCacheTTL)
	if err != nil {
		return chatgptCloudTurnResult{}, fmt.Errorf("resolve idempotent follow-up state: %w", err)
	}
	if existingText, found := chatgptCloudMessageTextByID(detail, requestMessageID); found {
		if existingText != prompt {
			return chatgptCloudTurnResult{}, fmt.Errorf("idempotencyKey was reused with different session.send content")
		}
		inheritedModel, inheritedThinking := chatgptCloudInitialSelection(detail)
		return chatgptCloudTurnResult{ConversationID: conversationID, Model: firstNonEmptyString(strings.TrimSpace(model), inheritedModel), Thinking: firstNonEmptyString(strings.TrimSpace(thinking), inheritedThinking), Replayed: true}, nil
	}
	if parentMessageID == "" {
		parentMessageID = chatgptCloudLastAssistantID(detail)
		if parentMessageID == "" {
			parentMessageID = chatgptCloudLastMessageID(detail)
		}
	}
	if parentMessageID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("could not resolve a parent message for the conversation")
	}
	inheritedModel, inheritedThinking := chatgptCloudInitialSelection(detail)
	if strings.TrimSpace(model) == "" {
		model = inheritedModel
	}
	if strings.TrimSpace(thinking) == "" {
		thinking = inheritedThinking
	}
	body := chatgptFollowUpBodyWithThinking(conversationID, parentMessageID, prompt, model, thinking)
	if err := chatgptCloudSetRequestMessageID(body, requestMessageID); err != nil {
		return chatgptCloudTurnResult{}, err
	}
	var result chatgptCloudTurnResult
	a.InvalidateRead(conversationID)
	defer a.InvalidateRead(conversationID)
	if a.sendOverride != nil {
		result, err = a.sendOverride(ctx, conversationID, parentMessageID, prompt, model, thinking)
	} else {
		result, err = a.createQuickBody(ctx, body)
	}
	if err != nil {
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatgptReconcileReadTimeout)
		reconciled, reconcileErr := a.ReadFresh(withChatGPTCloudReadSource(reconcileCtx, "send_reconcile"), conversationID)
		cancel()
		if reconcileErr == nil {
			if existingText, found := chatgptCloudMessageTextByID(reconciled, requestMessageID); found && existingText == prompt {
				err = nil
				result.Replayed = true
			}
		}
	}
	if result.ConversationID == "" {
		result.ConversationID = conversationID
	}
	result.Model = strings.TrimSpace(model)
	result.Thinking = strings.TrimSpace(thinking)
	return result, err
}

func (a *ChatGPTCloudAdapter) followUpBody(ctx context.Context, conversationID, parentMessageID, prompt, model, thinking string) (map[string]any, string, string, string, error) {
	if conversationID == "" {
		return nil, "", "", "", fmt.Errorf("conversationId is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, "", "", "", fmt.Errorf("message text is required")
	}
	if parentMessageID == "" || strings.TrimSpace(model) == "" || strings.TrimSpace(thinking) == "" {
		detail, err := a.ReadCached(withChatGPTCloudReadSource(ctx, "send_parent"), conversationID, chatgptCloudProviderReadCacheTTL)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("resolve follow-up state: %w", err)
		}
		if parentMessageID == "" {
			parentMessageID = chatgptCloudLastAssistantID(detail)
			if parentMessageID == "" {
				parentMessageID = chatgptCloudLastMessageID(detail)
			}
		}
		inheritedModel, inheritedThinking := chatgptCloudInitialSelection(detail)
		if strings.TrimSpace(model) == "" {
			model = inheritedModel
		}
		if strings.TrimSpace(thinking) == "" {
			thinking = inheritedThinking
		}
	}
	if parentMessageID == "" {
		return nil, "", "", "", fmt.Errorf("could not resolve a parent message for the conversation")
	}
	return chatgptFollowUpBodyWithThinking(conversationID, parentMessageID, prompt, model, thinking), parentMessageID, model, thinking, nil
}

// Steer appends a correction to an active compatible TPP turn through
// /backend-api/f/steer_turn. Ordinary completed ChatGPT conversations do not
// have an async_task_id and are rejected with a precise error instead of
// silently treating a Codex turnId as a cloud task identifier.
func (a *ChatGPTCloudAdapter) Steer(ctx context.Context, conversationID, asyncTaskID, prompt string) (chatgptCloudTurnResult, error) {
	conversationID = strings.TrimSpace(conversationID)
	asyncTaskID = strings.TrimSpace(asyncTaskID)
	prompt = strings.TrimSpace(prompt)
	if conversationID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("conversationId is required")
	}
	if prompt == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("message text is required")
	}

	detail, err := a.Read(ctx, conversationID)
	if err != nil {
		return chatgptCloudTurnResult{}, fmt.Errorf("resolve steer state: %w", err)
	}
	if asyncTaskID == "" {
		asyncTaskID = chatgptCloudSteerTaskID(detail)
	}
	if asyncTaskID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("no active steerable turn: asyncTaskId is unavailable for this conversation")
	}

	parentMessageID := chatgptCloudLastAssistantID(detail)
	if parentMessageID == "" {
		parentMessageID = chatgptCloudLastMessageID(detail)
	}
	if parentMessageID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("could not resolve a parent message for the steerable conversation")
	}
	model := mapString(detail, "model")
	body := chatgptFollowUpBody(conversationID, parentMessageID, prompt, model)
	body["async_task_id"] = asyncTaskID
	chatgptCloudApplySteerMetadata(body, detail)
	result, err := a.sendTurnTo(ctx, chatgptSteerTurnPath, body)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	if result.ConversationID == "" {
		result.ConversationID = conversationID
	}
	if result.AsyncTaskID == "" {
		result.AsyncTaskID = asyncTaskID
	}
	return result, nil
}

// Read fetches fresh conversation detail through the adapter read budget. It
// still coalesces concurrent reads for the same conversation and always obeys
// the adapter-wide pacing and rate-limit cooldown.
func (a *ChatGPTCloudAdapter) Read(ctx context.Context, conversationID string) (map[string]any, error) {
	return a.ReadFresh(ctx, conversationID)
}

// ReadCached returns conversation detail when a successful read newer than
// maxAge is available. A non-positive maxAge forces a fresh read, while still
// respecting the shared read budget.
func (a *ChatGPTCloudAdapter) ReadCached(ctx context.Context, conversationID string, maxAge time.Duration) (map[string]any, error) {
	return a.readConversation(ctx, conversationID, maxAge)
}

// ReadFresh obtains one fresh provider read. It cannot bypass pacing or a
// provider rate-limit cooldown; use InvalidateRead for mutation/event fencing.
func (a *ChatGPTCloudAdapter) ReadFresh(ctx context.Context, conversationID string) (map[string]any, error) {
	return a.readConversation(ctx, conversationID, 0)
}

// InvalidateRead drops the adapter's cached detail for one conversation. It is
// intended for send/mutation paths and never cancels an in-flight read.
func (a *ChatGPTCloudAdapter) InvalidateRead(conversationID string) {
	if a == nil {
		return
	}
	a.ensureReadBudget().invalidate(strings.TrimSpace(conversationID))
}

func (a *ChatGPTCloudAdapter) ensureReadBudget() *chatGPTCloudProviderReadBudget {
	a.readBudgetMu.Lock()
	defer a.readBudgetMu.Unlock()
	if a.readBudget == nil {
		a.readBudget = newChatGPTCloudProviderReadBudget(a.logger)
	}
	return a.readBudget
}

func (a *ChatGPTCloudAdapter) readConversation(ctx context.Context, conversationID string, maxAge time.Duration) (map[string]any, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("conversationId is required")
	}
	if a == nil {
		return nil, fmt.Errorf("chatgpt_cloud adapter is unavailable")
	}
	return a.ensureReadBudget().read(ctx, conversationID, maxAge, a.readConversationDirect)
}

func (a *ChatGPTCloudAdapter) readConversationDirect(ctx context.Context, conversationID string) (map[string]any, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	url := a.baseURL + strings.ReplaceAll(chatgptConversationDetail, "{id}", conversationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	chatgptApplyCloudHeaders(req, token)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, a.providerHTTPError("read conversation", resp)
	}
	var detail map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, err
	}
	return chatgptNormalizeDetail(detail), nil
}

// Models returns the ChatGPT cloud chat models (from /backend-api/models) — the
// model selection surface for chatgpt_cloud conversations, distinct from the
// Codex/work app-server model list.
func (a *ChatGPTCloudAdapter) Models(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := a.token(ctx)
	if err != nil {
		return nil, &chatGPTCatalogError{code: classifyChatGPTCloudAuthError(err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/backend-api/models", nil)
	if err != nil {
		return nil, err
	}
	chatgptApplyCloudHeaders(req, token)
	resp, err := a.http.Do(req)
	if err != nil {
		code := "CHATGPT_CLOUD_NETWORK_FAILED"
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			code = "CHATGPT_CLOUD_NETWORK_TIMEOUT"
		}
		return nil, &chatGPTCatalogError{code: code}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &chatGPTCatalogError{code: "CHATGPT_CLOUD_MODELS_HTTP_ERROR", status: resp.StatusCode}
	}
	var out struct {
		Title            string           `json:"title"`
		DefaultModelSlug string           `json:"default_model_slug"`
		Models           []map[string]any `json:"models"`
		SliderSettings   []map[string]any `json:"slider_settings"`
		Versions         []map[string]any `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &chatGPTCatalogError{code: "CHATGPT_CLOUD_MODELS_INVALID_RESPONSE"}
	}
	models := make([]map[string]any, 0, len(out.Models))
	seenModels := make(map[string]struct{}, len(out.Models))
	for _, raw := range out.Models {
		slug, _ := raw["slug"].(string)
		if slug == "" {
			continue
		}
		if _, exists := seenModels[slug]; exists {
			continue
		}
		seenModels[slug] = struct{}{}
		model := map[string]any{
			"id":          slug,
			"slug":        slug,
			"title":       chatgptCloudModelDisplayTitle(slug, mapString(raw, "title")),
			"description": mapString(raw, "description"),
		}
		if maxTokens, ok := raw["max_tokens"].(float64); ok {
			model["maxTokens"] = int64(maxTokens)
		}
		models = append(models, model)
	}
	presets := chatgptCloudModelPresets(out.Versions, out.SliderSettings)
	return map[string]any{
		"models":       models,
		"defaultModel": out.DefaultModelSlug,
		"creationModes": []map[string]any{
			{"id": "quick_chat", "title": "Quick chat", "returnPhase": "running"},
			{"id": "complete", "title": "Wait for first answer", "returnPhase": "ready"},
		},
		"thinkingOptions": chatGPTThinkingOptions(presets),
		"modelPresets":    presets,
	}, nil
}

// Preserve provider HTTP failures through the Node capability boundary. Do not
// turn throttling into an invalid caller request or expose provider response bodies.
type chatGPTCloudHTTPError struct {
	operation  string
	status     int
	retryAfter string
}

func (a *ChatGPTCloudAdapter) providerHTTPError(operation string, resp *http.Response) *chatGPTCloudHTTPError {
	if resp.StatusCode == http.StatusTooManyRequests {
		a.ensureReadBudget().noteRateLimit(resp.Header.Get("Retry-After"))
	}
	return newChatGPTCloudHTTPError(operation, resp)
}

func newChatGPTCloudHTTPError(operation string, resp *http.Response) *chatGPTCloudHTTPError {
	e := &chatGPTCloudHTTPError{operation: operation, status: resp.StatusCode}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		e.retryAfter = strconv.FormatInt(seconds, 10) + " seconds"
	} else if deadline, err := http.ParseTime(value); err == nil {
		e.retryAfter = deadline.UTC().Format(time.RFC3339)
	}
	return e
}

func (e *chatGPTCloudHTTPError) Error() string {
	message := fmt.Sprintf("ChatGPT Cloud %s returned HTTP %d", e.operation, e.status)
	if e.retryAfter != "" {
		message += "; Retry-After: " + e.retryAfter
	}
	return message
}

func (e *chatGPTCloudHTTPError) CapabilityError() (string, string, bool) {
	code := "CHATGPT_CLOUD_HTTP_ERROR"
	switch e.status {
	case http.StatusTooManyRequests:
		code = "CHATGPT_CLOUD_RATE_LIMITED"
	case http.StatusUnauthorized:
		code = "CHATGPT_CLOUD_UNAUTHORIZED"
	case http.StatusForbidden:
		code = "CHATGPT_CLOUD_FORBIDDEN"
	case http.StatusNotFound:
		code = "CHATGPT_CLOUD_CONVERSATION_NOT_FOUND"
	}
	return code, e.Error(), e.status == http.StatusTooManyRequests || e.status >= 500
}

// Keep provider bodies, credentials, URLs and raw transport errors out of UI
// and capability responses while preserving the failed stage and HTTP status.
type chatGPTCatalogError struct {
	code   string
	status int
}

func (e *chatGPTCatalogError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("ChatGPT model catalog request returned HTTP %d", e.status)
	}
	return e.code
}

func (e *chatGPTCatalogError) CapabilityError() (string, string, bool) {
	code := e.code
	switch e.status {
	case http.StatusUnauthorized:
		code = "CHATGPT_CLOUD_MODELS_UNAUTHORIZED"
	case http.StatusForbidden:
		code = "CHATGPT_CLOUD_MODELS_FORBIDDEN"
	case http.StatusTooManyRequests:
		code = "CHATGPT_CLOUD_MODELS_RATE_LIMITED"
	}
	retryable := e.status == http.StatusTooManyRequests || e.status >= 500 || code == "CHATGPT_CLOUD_NETWORK_TIMEOUT" || code == "CHATGPT_CLOUD_NETWORK_FAILED" || code == "CHATGPT_CLOUD_AUTH_RPC_TIMEOUT"
	return code, e.Error(), retryable
}

func chatgptCloudModelDisplayTitle(slug, providerTitle string) string {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "gpt-5-6":
		return "GPT-5.6"
	case "gpt-5-6-instant":
		return "GPT-5.6 Instant"
	case "gpt-5-6-thinking":
		return "GPT-5.6 Thinking"
	case "gpt-5-6-pro":
		return "GPT-5.6 Pro"
	case "gpt-5-5":
		return "GPT-5.5"
	case "gpt-5-5-instant":
		return "GPT-5.5 Instant"
	case "gpt-5-5-thinking":
		return "GPT-5.5 Thinking"
	case "gpt-5-5-pro":
		return "GPT-5.5 Pro"
	default:
		return firstNonEmptyString(strings.TrimSpace(providerTitle), strings.TrimSpace(slug))
	}
}

func chatgptCloudModelPresets(versions, sliderSettings []map[string]any) []map[string]any {
	presets := make([]map[string]any, 0)
	seen := map[string]bool{}
	appendPreset := func(versionID string, raw map[string]any) {
		model := mapString(raw, "model_slug")
		if model == "" {
			return
		}
		thinking := mapString(raw, "thinking_effort")
		lane := mapString(raw, "lane")
		key := versionID + "\x00" + lane + "\x00" + model + "\x00" + thinking
		if seen[key] {
			return
		}
		seen[key] = true
		preset := map[string]any{
			"model": model,
			"title": firstNonEmptyString(mapString(raw, "selected_display_title"), mapString(raw, "title"), model),
		}
		if versionID != "" {
			preset["version"] = versionID
		}
		if lane != "" {
			preset["lane"] = lane
		}
		if thinking != "" {
			preset["thinking"] = thinking
		}
		presets = append(presets, preset)
	}
	for _, version := range versions {
		versionID := mapString(version, "id")
		rawPresets, _ := version["intelligence_presets"].([]any)
		for _, raw := range rawPresets {
			preset, _ := raw.(map[string]any)
			appendPreset(versionID, preset)
		}
	}
	for _, setting := range sliderSettings {
		appendPreset("", setting)
	}
	return presets
}

// List returns the most recently updated conversations.
func (a *ChatGPTCloudAdapter) List(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	authTimeout := a.listAuthTimeout
	if authTimeout <= 0 {
		authTimeout = chatgptListAuthTimeout
	}
	authCtx, cancelAuth := context.WithTimeout(ctx, authTimeout)
	token, err := a.token(authCtx)
	cancelAuth()
	if err != nil {
		return nil, fmt.Errorf("list conversations auth: %w", err)
	}
	requestTimeout := a.listRequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = chatgptListRequestTimeout
	}
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestTimeout)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, a.baseURL+chatgptConversationsList+"?limit="+fmt.Sprintf("%d", limit)+"&order=updated", nil)
	if err != nil {
		return nil, err
	}
	chatgptApplyCloudHeaders(req, token)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list conversations provider request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list conversations returned %s", resp.Status)
	}
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rawBody, &wrapped); err == nil && wrapped.Items != nil {
		return wrapped.Items, nil
	}
	var plain []map[string]any
	if err := json.Unmarshal(rawBody, &plain); err == nil {
		return plain, nil
	}
	return nil, fmt.Errorf("list conversations returned an unsupported response shape")
}

func (a *ChatGPTCloudAdapter) FindConversationByMessageID(ctx context.Context, requestMessageID string) (string, bool, error) {
	requestMessageID = strings.TrimSpace(requestMessageID)
	if requestMessageID == "" {
		return "", false, fmt.Errorf("request message id is required")
	}
	items, err := a.List(ctx, chatgptReconcileListLimit)
	if err != nil {
		return "", false, err
	}
	matches := map[string]struct{}{}
	for _, item := range items {
		sessionID := chatgptCloudListSessionID(item)
		if sessionID == "" {
			continue
		}
		if chatgptCloudContainsMessageID(item, requestMessageID) {
			matches[sessionID] = struct{}{}
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, chatgptReconcileReadTimeout)
		detail, readErr := a.Read(readCtx, sessionID)
		cancel()
		if readErr == nil && chatgptCloudContainsMessageID(detail, requestMessageID) {
			matches[sessionID] = struct{}{}
		}
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("multiple ChatGPT cloud conversations contain the same request message id")
	}
	for sessionID := range matches {
		return sessionID, true, nil
	}
	return "", false, nil
}

func chatgptCloudContainsMessageID(value map[string]any, requestMessageID string) bool {
	mapping, _ := value["mapping"].(map[string]any)
	if _, ok := mapping[requestMessageID]; ok {
		return true
	}
	for key, raw := range mapping {
		if key == requestMessageID {
			return true
		}
		node, _ := raw.(map[string]any)
		if mapString(node, "id") == requestMessageID {
			return true
		}
		message, _ := node["message"].(map[string]any)
		if mapString(message, "id") == requestMessageID {
			return true
		}
	}
	return false
}

func chatgptCloudMessageTextByID(value map[string]any, requestMessageID string) (string, bool) {
	mapping, _ := value["mapping"].(map[string]any)
	for nodeID, raw := range mapping {
		node, _ := raw.(map[string]any)
		message, _ := node["message"].(map[string]any)
		if nodeID != requestMessageID && mapString(node, "id") != requestMessageID && mapString(message, "id") != requestMessageID {
			continue
		}
		content, _ := message["content"].(map[string]any)
		var builder strings.Builder
		switch parts := content["parts"].(type) {
		case []string:
			builder.WriteString(strings.Join(parts, ""))
		case []any:
			for _, part := range parts {
				if text, ok := part.(string); ok {
					builder.WriteString(text)
				}
			}
		}
		return strings.TrimSpace(builder.String()), true
	}
	return "", false
}

// Rename sets a conversation title.
func (a *ChatGPTCloudAdapter) Rename(ctx context.Context, conversationID, title string) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"title": title})
	url := a.baseURL + strings.ReplaceAll(chatgptConversationDetail, "{id}", conversationID)
	for attempt := 0; attempt < 10; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		chatgptApplyCloudHeaders(req, token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.http.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if resp.StatusCode != http.StatusNotFound || attempt == 9 {
			return fmt.Errorf("rename conversation returned %s", resp.Status)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("rename conversation did not complete")
}

// Archive sets is_archived.
func (a *ChatGPTCloudAdapter) Archive(ctx context.Context, conversationID string, archived bool) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"is_archived": archived})
	url := a.baseURL + strings.ReplaceAll(chatgptConversationDetail, "{id}", conversationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("archive conversation returned %s", resp.Status)
	}
	return nil
}

// Delete removes a conversation.
func (a *ChatGPTCloudAdapter) Delete(ctx context.Context, conversationID string) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	url := a.baseURL + "/backend-api/conversation/id/" + conversationID
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	chatgptApplyCloudHeaders(req, token)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete conversation returned %s", resp.Status)
	}
	return nil
}

// Cancel stops the active turn in a conversation via /stop_conversation.
// If there is no active turn (the conversation already idled), 403 is treated as
// a no-op so cancel is idempotent.
func (a *ChatGPTCloudAdapter) Cancel(ctx context.Context, conversationID string) error {
	if conversationID == "" {
		return fmt.Errorf("conversationId is required")
	}
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	body := chatgptConversationBody("", "gpt-5-6", conversationID, "", "sent")
	raw, _ := json.Marshal(body)
	conduit := a.takeConduit(conversationID)
	if conduit == "" {
		prepared, err := a.prepare(ctx, token, body)
		if err != nil {
			return err
		}
		conduit = prepared
	}
	sentinel, err := chatgptSentinelHeaders(ctx, a.http, a.baseURL, token)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+chatgptStopPath, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	if conduit != "" {
		req.Header.Set("x-conduit-token", conduit)
	}
	for key, value := range sentinel {
		req.Header.Set(key, value)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil // no active turn to stop
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stop conversation returned %s", resp.Status)
	}
	return nil
}

// chatgptNormalizeDetail converts a cloud conversation detail into a thread-like
// map compatible with the codex session view (sessionId, title, mapping, ...).
func chatgptNormalizeDetail(detail map[string]any) map[string]any {
	out := map[string]any{
		"sessionId":   detail["conversation_id"],
		"title":       detail["title"],
		"model":       detail["default_model_slug"],
		"mapping":     detail["mapping"],
		"currentNode": detail["current_node"],
	}
	if detail["update_time"] != nil {
		out["updateTime"] = detail["update_time"]
	}
	if status, ok := chatgptCloudFirstMapValue(detail, "async_status", "conversation_async_status"); ok {
		out["asyncStatus"] = status
	}
	if taskID := chatgptCloudSteerTaskID(detail); taskID != "" {
		out["asyncTaskId"] = taskID
	}
	if exchangeID := chatgptCloudDetailString(detail, "turn_exchange_id", "turnExchangeId"); exchangeID != "" {
		out["turnExchangeId"] = exchangeID
	}
	if chimeVersion := chatgptCloudDetailValue(detail, "chime_version", "chimeVersion"); chimeVersion != nil {
		out["chimeVersion"] = chimeVersion
	}
	if status := chatgptCloudConversationStatus(detail); status != "" {
		out["status"] = status
	}
	return out
}

// chatgptCloudConversationStatus maps provider async/terminal facts to the
// provider-neutral session status vocabulary. Absence of a terminal fact is
// intentionally unknown; an existing assistant message alone is not proof
// that the latest turn completed.
func chatgptCloudConversationStatus(detail map[string]any) string {
	terminal := ""
	for _, key := range []string{"async_status", "asyncStatus", "conversation_async_status", "conversationAsyncStatus", "status"} {
		if value, ok := detail[key]; ok {
			if status := chatgptCloudStatusValue(value); status != "" {
				if status == "running" {
					return status
				}
				terminal = status
			}
		}
	}
	mapping, _ := detail["mapping"].(map[string]any)
	if current := chatgptCloudCurrentNodeID(detail); current != "" {
		seen := map[string]bool{}
		for current != "" && !seen[current] {
			seen[current] = true
			node, _ := mapping[current].(map[string]any)
			message, _ := node["message"].(map[string]any)
			if message != nil {
				// Only the current message can end this turn. Walking past a
				// user/tool/progress message would reuse a previous turn's final.
				status := chatgptCloudStatusValue(mapString(message, "status"))
				if status == "running" {
					return status
				}
				if terminal == "failed" || terminal == "canceled" {
					return terminal
				}
				if chatgptCloudFinalAssistantMessage(message) {
					return "completed"
				}
				return "unknown"
			}
			current = mapString(node, "parent")
		}
	}
	if terminal == "failed" || terminal == "canceled" {
		return terminal
	}
	return "unknown"
}

func chatgptCloudFinalAssistantMessage(message map[string]any) bool {
	end, _ := message["end_turn"].(bool)
	channel := mapString(message, "channel")
	recipient := mapString(message, "recipient")
	return chatgptCloudMessageRole(message) == "assistant" && end &&
		(channel == "" || channel == "final") && (recipient == "" || recipient == "all") &&
		chatgptCloudStatusValue(mapString(message, "status")) == "completed"
}

func chatgptCloudStatusValue(value any) string {
	switch typed := value.(type) {
	case string:
		status := strings.ToLower(strings.TrimSpace(typed))
		switch status {
		case "running", "in_progress", "in-progress", "queued", "pending", "processing", "generating", "active", "started":
			return "running"
		case "completed", "complete", "success", "succeeded", "done", "finished", "finished_successfully":
			return "completed"
		case "failed", "failure", "error", "errored":
			return "failed"
		case "canceled", "cancelled", "stopped", "aborted", "interrupted":
			return "canceled"
		}
	case map[string]any:
		for _, key := range []string{"status", "state", "phase", "turn_status", "turnStatus"} {
			if child, ok := typed[key]; ok {
				if status := chatgptCloudStatusValue(child); status != "" {
					return status
				}
			}
		}
	}
	return ""
}

func chatgptCloudInitialSelection(detail map[string]any) (string, string) {
	type candidate struct {
		created float64
		value   string
	}
	mapping, _ := detail["mapping"].(map[string]any)
	models := make([]candidate, 0, len(mapping))
	thinkingEfforts := make([]candidate, 0, len(mapping))
	for _, raw := range mapping {
		node, _ := raw.(map[string]any)
		message, _ := node["message"].(map[string]any)
		if message == nil || chatgptCloudMessageRole(message) != "assistant" {
			continue
		}
		metadata, _ := message["metadata"].(map[string]any)
		model := firstNonEmptyString(
			mapString(metadata, "requested_model_slug"),
			mapString(metadata, "default_model_slug"),
			mapString(metadata, "model_slug"),
			mapString(metadata, "resolved_model_slug"),
		)
		thinking := mapString(metadata, "thinking_effort")
		created, _ := message["create_time"].(float64)
		if model != "" {
			models = append(models, candidate{created: created, value: model})
		}
		if thinking != "" {
			thinkingEfforts = append(thinkingEfforts, candidate{created: created, value: thinking})
		}
	}
	sortCandidates := func(candidates []candidate) {
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].created == 0 {
				return false
			}
			if candidates[j].created == 0 {
				return true
			}
			return candidates[i].created < candidates[j].created
		})
	}
	sortCandidates(models)
	sortCandidates(thinkingEfforts)
	model := mapString(detail, "model")
	if len(models) > 0 {
		model = models[0].value
	}
	thinking := ""
	if len(thinkingEfforts) > 0 {
		thinking = thinkingEfforts[0].value
	}
	return model, thinking
}

// chatgptCloudLastAssistantID returns the id of the newest assistant message node.
func chatgptCloudLastAssistantID(detail map[string]any) string {
	mapping, _ := detail["mapping"].(map[string]any)
	if current := chatgptCloudCurrentNodeID(detail); current != "" {
		seen := map[string]bool{}
		for current != "" && !seen[current] {
			seen[current] = true
			node, _ := mapping[current].(map[string]any)
			if msg, _ := node["message"].(map[string]any); chatgptCloudMessageRole(msg) == "assistant" {
				if id := mapString(msg, "id"); id != "" {
					return id
				}
			}
			current = mapString(node, "parent")
		}
	}

	return chatgptCloudLatestNodeID(mapping, "assistant")
}

// chatgptCloudLastMessageID returns the current node id, falling back to any node.
func chatgptCloudLastMessageID(detail map[string]any) string {
	if current := chatgptCloudCurrentNodeID(detail); current != "" {
		return current
	}
	mapping, _ := detail["mapping"].(map[string]any)
	return chatgptCloudLatestNodeID(mapping, "")
}

func chatgptCloudCurrentNodeID(detail map[string]any) string {
	return firstNonEmptyString(mapString(detail, "currentNode"), mapString(detail, "current_node"))
}

func chatgptCloudMessageRole(message map[string]any) string {
	author, _ := message["author"].(map[string]any)
	return mapString(author, "role")
}

func chatgptCloudLatestNodeID(mapping map[string]any, role string) string {
	type candidate struct {
		id      string
		created float64
	}
	candidates := make([]candidate, 0, len(mapping))
	for id, raw := range mapping {
		node, _ := raw.(map[string]any)
		message, _ := node["message"].(map[string]any)
		if message == nil || (role != "" && chatgptCloudMessageRole(message) != role) {
			continue
		}
		created, _ := message["create_time"].(float64)
		candidateID := firstNonEmptyString(mapString(message, "id"), id)
		if candidateID != "" {
			candidates = append(candidates, candidate{id: candidateID, created: created})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].created == candidates[j].created {
			return candidates[i].id > candidates[j].id
		}
		return candidates[i].created > candidates[j].created
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].id
}

func chatgptCloudFirstMapValue(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func chatgptCloudDetailString(detail map[string]any, keys ...string) string {
	if value, ok := chatgptCloudFirstMapValue(detail, keys...); ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	if mapping, _ := detail["mapping"].(map[string]any); mapping != nil {
		if current := chatgptCloudCurrentNodeID(detail); current != "" {
			if node, _ := mapping[current].(map[string]any); node != nil {
				if message, _ := node["message"].(map[string]any); message != nil {
					if metadata, _ := message["metadata"].(map[string]any); metadata != nil {
						if value, ok := chatgptCloudFirstMapValue(metadata, keys...); ok {
							if text, ok := value.(string); ok {
								return strings.TrimSpace(text)
							}
						}
					}
				}
			}
		}
		for id := range mapping {
			if node, _ := mapping[id].(map[string]any); node != nil {
				if message, _ := node["message"].(map[string]any); message != nil {
					if metadata, _ := message["metadata"].(map[string]any); metadata != nil {
						if value, ok := chatgptCloudFirstMapValue(metadata, keys...); ok {
							if text, ok := value.(string); ok {
								return strings.TrimSpace(text)
							}
						}
					}
				}
			}
		}
	}
	return ""
}

func chatgptCloudDetailValue(detail map[string]any, keys ...string) any {
	if value, ok := chatgptCloudFirstMapValue(detail, keys...); ok {
		return value
	}
	if mapping, _ := detail["mapping"].(map[string]any); mapping != nil {
		ids := []string{}
		if current := chatgptCloudCurrentNodeID(detail); current != "" {
			ids = append(ids, current)
		}
		if assistant := chatgptCloudLastAssistantID(detail); assistant != "" {
			ids = append(ids, assistant)
		}
		for _, id := range ids {
			node, _ := mapping[id].(map[string]any)
			message, _ := node["message"].(map[string]any)
			metadata, _ := message["metadata"].(map[string]any)
			if value, ok := chatgptCloudFirstMapValue(metadata, keys...); ok {
				return value
			}
		}
	}
	return nil
}

func chatgptCloudAsyncTaskIDInValue(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"async_task_id", "asyncTaskId"} {
			if taskID, ok := typed[key].(string); ok && strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(taskID)
			}
		}
		for _, child := range typed {
			if taskID := chatgptCloudAsyncTaskIDInValue(child); taskID != "" {
				return taskID
			}
		}
	case []any:
		for _, child := range typed {
			if taskID := chatgptCloudAsyncTaskIDInValue(child); taskID != "" {
				return taskID
			}
		}
	}
	return ""
}

func chatgptCloudSteerTaskID(detail map[string]any) string {
	for _, key := range []string{"async_task_id", "asyncTaskId"} {
		if taskID, ok := detail[key].(string); ok && strings.TrimSpace(taskID) != "" {
			return strings.TrimSpace(taskID)
		}
	}
	for _, key := range []string{"async_status", "asyncStatus", "conversation_async_status", "conversationAsyncStatus"} {
		status, ok := detail[key]
		if !ok || status == nil {
			continue
		}
		if taskID := chatgptCloudAsyncTaskIDInValue(status); taskID != "" {
			return taskID
		}
		// Historical completed image-generation messages can retain an
		// async_task_id even when the conversation itself is idle. Only use
		// message metadata as a fallback when the async status is structured,
		// which is the shape used by an active TPP status event.
		if _, structured := status.(map[string]any); !structured {
			continue
		}
		mapping, _ := detail["mapping"].(map[string]any)
		if taskID := chatgptCloudAsyncTaskIDInValue(mapping); taskID != "" {
			return taskID
		}
	}
	return ""
}

func chatgptCloudCaptureTurnMetadata(result *chatgptCloudTurnResult, message map[string]any) {
	if result == nil {
		return
	}
	metadata, _ := message["metadata"].(map[string]any)
	if result.AsyncTaskID == "" {
		result.AsyncTaskID = chatgptCloudAsyncTaskIDInValue(metadata)
	}
	if result.TurnExchangeID == "" {
		result.TurnExchangeID = chatgptCloudDetailString(metadata, "turn_exchange_id", "turnExchangeId", "working_turn_id")
	}
	if result.ChimeVersion == nil {
		result.ChimeVersion = chatgptCloudDetailValue(metadata, "chime_version", "chimeVersion")
	}
}

func chatgptCloudApplySteerMetadata(body, detail map[string]any) {
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		return
	}
	message, _ := messages[0].(map[string]any)
	metadata, _ := message["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		message["metadata"] = metadata
	}
	if exchangeID := chatgptCloudDetailString(detail, "turn_exchange_id", "turnExchangeId", "working_turn_id"); exchangeID != "" {
		metadata["turn_exchange_id"] = exchangeID
	}
	if chimeVersion := chatgptCloudDetailValue(detail, "chime_version", "chimeVersion"); chimeVersion != nil {
		metadata["chime_version"] = chimeVersion
	}
}
