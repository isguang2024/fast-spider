//go:build codexe2e

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChatGPTCloudServiceTierRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CHATGPT_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CHATGPT_E2E=1 to run the real ChatGPT service-tier comparison")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if _, err := manager.codex.Availability(ctx); err != nil {
		t.Skipf("Codex app-server unavailable: %v", err)
	}

	prompt := `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。数量如下：苹果味圆形 7、五角星形 7；桃子味圆形 9、五角星形 6；西瓜味圆形 8、五角星形 4。最少取出多少个糖果，才能保证手中同时拥有不同形状的苹果味和桃子味糖？圆形苹果味配五角星桃子味，或圆形桃子味配五角星苹果味，均算满足。请先单独用阿拉伯数字写出答案，再用不超过三句说明理由。`
	create := func(name, serviceTier string) (map[string]any, time.Duration) {
		params := map[string]any{
			"providerId": "codex", "backend": sessionBackendChatGPTCloud, "visibility": "visible",
			"mode": "complete", "model": "gpt-5-6-thinking", "prompt": prompt, "workingDirectory": workingDirectory,
			"thinking":       "max",
			"idempotencyKey": fmt.Sprintf("service-tier-e2e-%s-%d", name, time.Now().UnixNano()),
		}
		if serviceTier != "" {
				params["service_tier"] = serviceTier
		}
		started := time.Now()
		created, err := manager.Control(ctx, "session.create", params)
		if err != nil {
			t.Fatalf("%s create: %v", name, err)
		}
		if sessionID := mapString(created, "sessionId"); sessionID == "" {
			t.Fatalf("%s create returned no sessionId: %#v", name, created)
		}
		if mapString(created, "externalIdType") != "chatgpt_conversation" {
			t.Fatalf("%s create did not make a ChatGPT conversation: %#v", name, created)
		}
			if got := mapString(created, "service_tier"); got != serviceTier {
				t.Fatalf("%s service_tier=%q want %q: %#v", name, got, serviceTier, created)
		}
		return created, time.Since(started)
	}
	readAnswer := func(name string, created map[string]any) string {
		deadline := time.Now().Add(90 * time.Second)
		for {
			result, err := manager.Control(ctx, "session.get", map[string]any{
				"providerId": "codex", "backend": sessionBackendChatGPTCloud,
				"sessionId": mapString(created, "sessionId"), "limit": 8,
			})
			if err != nil {
				t.Fatalf("%s read answer: %v", name, err)
			}
			session, _ := result["session"].(map[string]any)
			messages, _ := session["messages"].([]map[string]any)
			for index := len(messages) - 1; index >= 0; index-- {
				if mapString(messages[index], "role") == "assistant" && mapString(messages[index], "text") != "" {
					return mapString(messages[index], "text")
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s read no assistant answer within 90 seconds: %#v", name, result)
			}
			time.Sleep(2 * time.Second)
		}
	}

	baseline, baselineElapsed := create("baseline", "")
	priority, priorityElapsed := create("priority", "priority")
	baselineAnswer := readAnswer("baseline", baseline)
	priorityAnswer := readAnswer("priority", priority)
	t.Logf("baseline session=%s elapsed=%s", mapString(baseline, "sessionId"), baselineElapsed.Round(time.Millisecond))
	t.Logf("baseline answer=%q", baselineAnswer)
	t.Logf("priority session=%s elapsed=%s", mapString(priority, "sessionId"), priorityElapsed.Round(time.Millisecond))
	t.Logf("priority answer=%q", priorityAnswer)
}

// TestChatGPTCloudAdapterRealE2E creates a real cloud conversation through the
// desktop app-server's ChatGPT token. Requires:
//   - the local Codex app-server logged into ChatGPT (getAuthStatus returns a token)
//   - FAST_SPIDER_CHATGPT_E2E=1
func TestChatGPTCloudAdapterRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CHATGPT_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CHATGPT_E2E=1 to run the real ChatGPT cloud conversation test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	codex := NewCodexAdapter(nil)
	defer codex.Close(context.Background())
	if _, err := codex.Availability(ctx); err != nil {
		t.Skipf("Codex app-server unavailable: %v", err)
	}

	adapter := NewChatGPTCloudAdapter(nil, func(c context.Context) (string, error) {
		return codex.AuthToken(c)
	})

	// 1. create + first message
	created, err := adapter.Create(ctx, "Go 适配器全链路测试：请只回复：收到。", "gpt-5-6")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ConversationID == "" {
		t.Fatal("Create returned no conversation id")
	}
	t.Logf("created conversation %s (inline messages=%d)", created.ConversationID, len(created.Messages))

	// 2. read detail
	detail, err := adapter.Read(ctx, created.ConversationID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	title, _ := detail["title"].(string)
	t.Logf("read title=%q model=%v", title, detail["model"])

	// 3. send follow-up
	parent := chatgptCloudLastAssistantID(detail)
	if parent == "" {
		parent = chatgptCloudLastMessageID(detail)
	}
	followed, err := adapter.Send(ctx, created.ConversationID, parent, "第二条：Go 适配器测试：请只回复：收到2。", "gpt-5-6")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Logf("follow-up ok, conversation=%s", followed.ConversationID)

	// 4. read again -> both turns present
	detail2, err := adapter.Read(ctx, created.ConversationID)
	if err != nil {
		t.Fatalf("Read after follow-up: %v", err)
	}
	texts := chatgptCloudDetailTexts(detail2)
	joined := strings.Join(texts, " ")
	t.Logf("detail texts: %v", texts)
	if !strings.Contains(joined, "第二条") {
		t.Errorf("follow-up message missing from detail: %v", texts)
	}

	// 5. list -> appears in cloud list
	items, err := adapter.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, item := range items {
		if id, _ := item["id"].(string); id == created.ConversationID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created conversation %s not found in cloud list", created.ConversationID)
	} else {
		t.Logf("conversation present in cloud list")
	}

	// 6. cleanup
	if err := adapter.Delete(ctx, created.ConversationID); err != nil {
		t.Logf("cleanup delete: %v", err)
	}
}

func chatgptCloudDetailTexts(detail map[string]any) []string {
	mapping, _ := detail["mapping"].(map[string]any)
	var texts []string
	for _, raw := range mapping {
		node, _ := raw.(map[string]any)
		msg, _ := node["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(safeString(p))
		}
		if sb.Len() > 0 {
			author, _ := msg["author"].(map[string]any)
			role, _ := author["role"].(string)
			texts = append(texts, role+": "+sb.String())
		}
	}
	return texts
}

func safeString(v any) string {
	s, _ := v.(string)
	return s
}

// TestChatGPTCloudManagerRealE2E drives the full ai_control surface for
// backend=chatgpt_cloud through Control().
func TestChatGPTCloudManagerRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CHATGPT_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CHATGPT_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	if _, err := m.codex.Availability(ctx); err != nil {
		t.Skipf("Codex app-server unavailable: %v", err)
	}

	// chat model selection is distinct from the Codex/work model list
	modelsRes, err := m.Control(ctx, "models.list", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
	})
	if err != nil {
		t.Fatalf("models.list: %v", err)
	}
	if source, _ := modelsRes["modelSource"].(string); source != "chatgpt_cloud" {
		t.Errorf("modelSource=%q want chatgpt_cloud", source)
	}
	models, _ := modelsRes["models"].([]map[string]any)
	if len(models) == 0 {
		t.Fatal("models.list returned no chat models")
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if id, _ := model["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	t.Logf("chat models (%d): %v", len(ids), ids[:min(8, len(ids))])

	created, err := m.Control(ctx, "session.create", map[string]any{
		"providerId":     "codex",
		"backend":        sessionBackendChatGPTCloud,
		"prompt":         "Manager 全链路：请只回复：收到。",
		"model":          "gpt-5-6",
		"idempotencyKey": "chatgpt-live-manager-create-001",
	})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	sid, _ := created["sessionId"].(string)
	if sid == "" {
		t.Fatal("session.create returned no sessionId")
	}
	if ext, _ := created["externalIdType"].(string); ext != "chatgpt_conversation" {
		t.Fatalf("externalIdType=%q want chatgpt_conversation", ext)
	}
	t.Logf("created cloud session %s externalIdType=%v", sid, created["externalIdType"])

	if _, err := m.Control(ctx, "session.send", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
		"sessionId":  sid,
		"prompt":     "第二条：请只回复：收到2。",
	}); err != nil {
		t.Fatalf("session.send: %v", err)
	}

	// A normal completed cloud chat has no active TPP turn. If a caller has an
	// active compatible task from a separate TPP flow, it can be supplied through
	// FAST_SPIDER_CHATGPT_STEER_TASK_ID to exercise the real steer endpoint.
	steerTaskID := strings.TrimSpace(os.Getenv("FAST_SPIDER_CHATGPT_STEER_TASK_ID"))
	steerParams := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": sid, "prompt": "请把当前活动任务改为只回复：收到 steer。",
	}
	if steerTaskID != "" {
		steerParams["asyncTaskId"] = steerTaskID
	}
	steerResult, steerErr := m.Control(ctx, "session.steer", steerParams)
	if steerTaskID == "" {
		if steerErr == nil || !strings.Contains(steerErr.Error(), "no active steerable turn") {
			t.Fatalf("session.steer without active task error=%v result=%#v", steerErr, steerResult)
		}
		t.Logf("session.steer correctly rejected completed ordinary chat: %v", steerErr)
	} else if steerErr != nil {
		t.Fatalf("session.steer: %v", steerErr)
	} else if steered, _ := steerResult["steered"].(bool); !steered {
		t.Fatalf("session.steer result=%#v", steerResult)
	}

	got, err := m.Control(ctx, "session.get", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
		"sessionId":  sid,
	})
	if err != nil {
		t.Fatalf("session.get: %v", err)
	}
	session, _ := got["session"].(map[string]any)
	if session == nil {
		t.Fatal("session.get returned no session")
	}
	t.Logf("session.get title=%v", session["title"])

	result, err := m.Control(ctx, "session.result", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
		"sessionId":  sid,
	})
	if err != nil {
		t.Fatalf("session.result: %v", err)
	}
	if msg, _ := result["finalAgentMessage"].(string); msg == "" {
		t.Errorf("session.result empty finalAgentMessage")
	} else {
		t.Logf("session.result final=%q", msg)
	}

	list, err := m.Control(ctx, "session.list", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
		"limit":      10,
	})
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	if sessions, _ := list["sessions"].([]map[string]any); len(sessions) == 0 {
		t.Errorf("session.list returned no cloud conversations")
	} else {
		t.Logf("session.list returned %d conversations", len(sessions))
	}

	if _, err := m.Control(ctx, "session.delete", map[string]any{
		"providerId": "codex",
		"backend":    sessionBackendChatGPTCloud,
		"sessionId":  sid,
	}); err != nil {
		t.Logf("session.delete: %v", err)
	}
}

// TestChatGPTCloudRealtimeRealE2E verifies session.watch (pubsub) + session.cancel.
func TestChatGPTCloudRealtimeRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CHATGPT_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CHATGPT_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	if _, err := m.codex.Availability(ctx); err != nil {
		t.Skipf("Codex app-server unavailable: %v", err)
	}

	created, err := m.Control(ctx, "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "实时测试：请只回复：实时OK。", "model": "gpt-5-6", "idempotencyKey": "chatgpt-live-realtime-create-001",
	})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	sid, _ := created["sessionId"].(string)
	if sid == "" {
		t.Fatal("session.create returned no sessionId")
	}

	// start watching (subscribes to pubsub)
	first, err := m.Control(ctx, "session.watch", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": sid, "waitSeconds": 0,
	})
	if err != nil {
		t.Fatalf("session.watch (start): %v", err)
	}
	cursor, _ := first["cursor"].(int64)
	t.Logf("watch started cursor=%d", cursor)

	// second client writes a follow-up -> should emit conversation.turn.complete
	if _, err := m.Control(ctx, "session.send", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": sid,
		"prompt": "第二客户端：请只回复：实时2。",
	}); err != nil {
		t.Fatalf("session.send: %v", err)
	}

	got, err := m.Control(ctx, "session.watch", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": sid,
		"cursor": cursor, "waitSeconds": 8,
	})
	if err != nil {
		t.Fatalf("session.watch (poll): %v", err)
	}
	events, _ := got["events"].([]map[string]any)
	t.Logf("watch events=%d", len(events))
	foundTurnComplete := false
	for _, e := range events {
		etype, _ := e["type"].(string)
		eventType, _ := e["eventType"].(string)
		t.Logf("  event type=%s raw=%s", etype, eventType)
		if etype == "conversation.turn.complete" {
			foundTurnComplete = true
		}
	}
	if !foundTurnComplete {
		t.Errorf("expected a conversation.turn.complete realtime event, got %v", events)
	}

	// cancel should not error
	if _, err := m.Control(ctx, "session.cancel", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": sid,
	}); err != nil {
		t.Logf("session.cancel: %v", err)
	}

	if _, err := m.Control(ctx, "session.delete", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": sid,
	}); err != nil {
		t.Logf("session.delete: %v", err)
	}
}
