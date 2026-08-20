package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const (
	chatgptCloudBaseURL        = "https://chatgpt.com"
	chatgptConversationPath    = "/backend-api/f/conversation"
	chatgptConversationPrepare = "/backend-api/f/conversation/prepare"
	chatgptConversationDetail  = "/backend-api/conversation/{id}"
	chatgptConversationsList   = "/backend-api/conversations"
	chatgptStopPath            = "/backend-api/stop_conversation"
	chatgptStreamTimeout       = 120 * time.Second
)

// ChatGPTCloudAdapter drives ChatGPT cloud conversations through the same
// /backend-api/f/conversation flow the official client uses, authenticating with
// the Codex app-server's ChatGPT token and solving the Sentinel challenge itself.
type ChatGPTCloudAdapter struct {
	logger      *slog.Logger
	baseURL     string
	http        *http.Client
	tokenSource func(ctx context.Context) (string, error)
	realtime    *chatgptCloudRealtime

	conduitMu             sync.Mutex
	conduitByConversation map[string]string
}

type chatgptCloudTurnResult struct {
	ConversationID string
	Messages       []chatgptCloudMessage
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
		conduitByConversation: map[string]string{},
	}
	adapter.realtime = newChatGPTCloudRealtime(logger, adapter.baseURL, adapter.http, tokenSource)
	return adapter
}

// WatchRealtime returns live pubsub events for a conversation after the cursor.
func (a *ChatGPTCloudAdapter) WatchRealtime(ctx context.Context, conversationID string, cursor int64, wait time.Duration) ([]chatgptCloudEvent, int64, error) {
	return a.realtime.watch(ctx, conversationID, cursor, wait)
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
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

// token resolves the desktop-app ChatGPT access token.
func (a *ChatGPTCloudAdapter) token(ctx context.Context) (string, error) {
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
	return token, nil
}

func chatgptUserMessage(text string) map[string]any {
	return map[string]any{
		"id":          chatgptCloudUUID(),
		"author":      map[string]any{"role": "user"},
		"create_time": float64(time.Now().UnixMilli()) / 1000,
		"content":     map[string]any{"content_type": "text", "parts": []string{text}},
		"metadata":    map[string]any{"selected_sources": []string{}, "serialization_metadata": map[string]any{"custom_symbol_offsets": []string{}}},
	}
}

func chatgptNewChatBody(prompt, model string) map[string]any {
	return chatgptConversationBody(prompt, model, "", "", "success")
}

func chatgptFollowUpBody(conversationID, parentMessageID, prompt, model string) map[string]any {
	return chatgptConversationBody(prompt, model, conversationID, parentMessageID, "sent")
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
	result, err := a.stream(ctx, token, conduit, body, sentinel)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	a.cacheConduit(result.ConversationID, conduit)
	return result, nil
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
		return "", fmt.Errorf("conversation prepare returned %s", resp.Status)
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
	raw, err := json.Marshal(body)
	if err != nil {
		return chatgptCloudTurnResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+chatgptConversationPath, strings.NewReader(string(raw)))
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
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return chatgptCloudTurnResult{}, fmt.Errorf("conversation stream returned %s: %s", resp.Status, strings.TrimSpace(string(rawBody)))
	}
	return chatgptParseStream(resp.Body, 20000)
}

// chatgptParseStream parses the /f/conversation SSE stream: collects the
// conversation id and any inline message events, stopping at message_stream_complete.
func chatgptParseStream(reader io.Reader, maxEvents int) (chatgptCloudTurnResult, error) {
	result := chatgptCloudTurnResult{}
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
			if cid, _ := data["conversation_id"].(string); cid != "" && result.ConversationID == "" {
				result.ConversationID = cid
			}
			if msg, ok := data["message"].(map[string]any); ok {
				result.Messages = append(result.Messages, chatgptCloudMessageFromMap(msg))
			}
			if input, ok := data["input_message"].(map[string]any); ok {
				result.Messages = append(result.Messages, chatgptCloudMessageFromMap(input))
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
				return result, nil
			}
		}
	}
	if len(dataLines) > 0 {
		flush()
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
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("creating a ChatGPT cloud conversation requires a first message")
	}
	return a.sendTurn(ctx, chatgptNewChatBody(prompt, model))
}

// Send appends a follow-up message to an existing conversation. If parentMessageID
// is empty it is resolved to the latest assistant message (like the official client).
func (a *ChatGPTCloudAdapter) Send(ctx context.Context, conversationID, parentMessageID, prompt, model string) (chatgptCloudTurnResult, error) {
	if conversationID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("conversationId is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("message text is required")
	}
	if parentMessageID == "" {
		detail, err := a.Read(ctx, conversationID)
		if err != nil {
			return chatgptCloudTurnResult{}, fmt.Errorf("resolve parent message: %w", err)
		}
		parentMessageID = chatgptCloudLastAssistantID(detail)
		if parentMessageID == "" {
			parentMessageID = chatgptCloudLastMessageID(detail)
		}
	}
	if parentMessageID == "" {
		return chatgptCloudTurnResult{}, fmt.Errorf("could not resolve a parent message for the conversation")
	}
	return a.sendTurn(ctx, chatgptFollowUpBody(conversationID, parentMessageID, prompt, model))
}

// Read fetches the conversation detail and normalizes it into a thread-like map.
func (a *ChatGPTCloudAdapter) Read(ctx context.Context, conversationID string) (map[string]any, error) {
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
		return nil, fmt.Errorf("read conversation returned %s", resp.Status)
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
func (a *ChatGPTCloudAdapter) Models(ctx context.Context) ([]map[string]any, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/backend-api/models", nil)
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
		return nil, fmt.Errorf("list chat models returned %s", resp.Status)
	}
	var out struct {
		Title  string           `json:"title"`
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]map[string]any, 0, len(out.Models))
	for _, raw := range out.Models {
		slug, _ := raw["slug"].(string)
		if slug == "" {
			continue
		}
		model := map[string]any{
			"id":          slug,
			"slug":        slug,
			"title":       firstNonEmptyString(mapString(raw, "title"), slug),
			"description": mapString(raw, "description"),
		}
		if maxTokens, ok := raw["max_tokens"].(float64); ok {
			model["maxTokens"] = int64(maxTokens)
		}
		models = append(models, model)
	}
	return models, nil
}

// List returns the most recently updated conversations.
func (a *ChatGPTCloudAdapter) List(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+chatgptConversationsList+"?limit="+fmt.Sprintf("%d", limit)+"&order=updated", nil)
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
	return []map[string]any{}, nil
}

// Rename sets a conversation title.
func (a *ChatGPTCloudAdapter) Rename(ctx context.Context, conversationID, title string) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"title": title})
	url := a.baseURL + strings.ReplaceAll(chatgptConversationDetail, "{id}", conversationID) + "/rename"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
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
		return fmt.Errorf("rename conversation returned %s", resp.Status)
	}
	return nil
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
		"status":      "completed",
		"mapping":     detail["mapping"],
		"currentNode": detail["current_node"],
	}
	if detail["update_time"] != nil {
		out["updateTime"] = detail["update_time"]
	}
	return out
}

// chatgptCloudLastAssistantID returns the id of the newest assistant message node.
func chatgptCloudLastAssistantID(detail map[string]any) string {
	mapping, _ := detail["mapping"].(map[string]any)
	var lastAssistant string
	for _, raw := range mapping {
		node, _ := raw.(map[string]any)
		msg, _ := node["message"].(map[string]any)
		if msg == nil {
			continue
		}
		author, _ := msg["author"].(map[string]any)
		if role, _ := author["role"].(string); role == "assistant" {
			if id, _ := msg["id"].(string); id != "" {
				lastAssistant = id
			}
		}
	}
	return lastAssistant
}

// chatgptCloudLastMessageID returns the current node id, falling back to any node.
func chatgptCloudLastMessageID(detail map[string]any) string {
	current, _ := detail["currentNode"].(string)
	if current != "" {
		return current
	}
	mapping, _ := detail["mapping"].(map[string]any)
	for id := range mapping {
		return id
	}
	return ""
}
