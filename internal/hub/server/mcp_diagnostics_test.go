package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMCPDiagnosticsAreBoundedIsolatedAndAllowlisted(t *testing.T) {
	started := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	store := newMCPDiagnosticsStore("0.4.16", started)
	for index := 0; index < maxMCPDiagnosticEvents+9; index++ {
		store.record("owner-a", "tools/call", "file_read", "codex", "success", "", started.Add(time.Duration(index)*time.Second))
	}
	store.record("owner-b", "initialize", "", "chatgpt", "success", "", started.Add(time.Hour))

	a := store.snapshot("owner-a")
	if len(a.RecentEvents) != maxMCPDiagnosticEvents || a.LastToolName != "file_read" || a.ClientType != "codex" || a.Result != "success" || a.Diagnosis != "tool_call_succeeded" {
		t.Fatalf("owner-a snapshot=%+v", a)
	}
	b := store.snapshot("owner-b")
	if len(b.RecentEvents) != 1 || b.LastInitializeAt == "" || b.LastToolName != "" || b.ClientType != "chatgpt" || b.Diagnosis != "initialized_no_tools_list" {
		t.Fatalf("owner-b snapshot=%+v", b)
	}
	missing := store.snapshot("owner-missing")
	if len(missing.RecentEvents) != 0 || missing.Diagnosis != "no_initialize" || missing.ServerStartedAt != started.Format(time.RFC3339) {
		t.Fatalf("missing-owner snapshot=%+v", missing)
	}

	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Authorization", "Bearer ", "Token", "Cookie", "arguments", "Prompt", "User-Agent", "C:/", "/home/", "owner-a"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("diagnostics leaked forbidden marker %q: %s", forbidden, raw)
		}
	}
}

func TestMCPDiagnosticsTrackAuthenticatedRequestWithoutCreatingFakeToolEvents(t *testing.T) {
	started := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	store := newMCPDiagnosticsStore("0.4.17", started)
	requestAt := started.Add(30 * time.Second)
	store.recordAuthenticatedRequest("owner-a", "chatgpt", requestAt)

	snapshot := store.snapshot("owner-a")
	if snapshot.LastMCPRequestAt != requestAt.Format(time.RFC3339) || snapshot.ClientType != "chatgpt" || snapshot.Diagnosis != "no_initialize" {
		t.Fatalf("authenticated request snapshot=%+v", snapshot)
	}
	if len(snapshot.RecentEvents) != 0 || snapshot.LastInitializeAt != "" || snapshot.LastToolsListAt != "" || snapshot.LastToolCallAt != "" {
		t.Fatalf("authenticated HTTP request was misreported as an MCP method event: %+v", snapshot)
	}
}

func TestMCPDiagnosticsNormalizeClientsAndStableErrors(t *testing.T) {
	for input, want := range map[string]string{
		"ChatGPT/1.0": "chatgpt", "OpenAI Connector": "chatgpt", "Codex Desktop": "codex", "mcpcli": "mcpcli", "unknown-client/9": "other", "": "other",
	} {
		if got := normalizeMCPClientName(input); got != want {
			t.Fatalf("normalize %q=%q want=%q", input, got, want)
		}
	}
	for _, code := range []string{"INVALID_REQUEST", "NOT_FOUND", "CONNECTION_LOST", "MACHINE_OFFLINE", "DEADLINE_EXCEEDED", "ABSOLUTE_PATH_REQUIRED", "BROWSER_REF_STALE", "NODE_UPDATING", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE", "JOB_NOT_FOUND"} {
		if got := stableMCPErrorCode(errors.New(code + ": redacted detail")); got != code {
			t.Fatalf("stable code %s=%s", code, got)
		}
	}
	if got := stableMCPErrorCode(errors.New("secret path C:/private/file failed")); got != "INTERNAL" {
		t.Fatalf("unexpected raw-error classification %q", got)
	}
}

func TestMCPDiagnosticsCarryRecognizedClientAcrossStatelessHandshakeWindow(t *testing.T) {
	started := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	store := newMCPDiagnosticsStore("0.4.16", started)
	store.record("owner-a", "initialize", "", "mcpcli", "success", "", started)
	store.record("owner-a", "tools/list", "", "other", "success", "", started.Add(time.Second))
	store.record("owner-a", "tools/call", "machine_list", "other", "success", "", started.Add(2*time.Second))
	if snapshot := store.snapshot("owner-a"); snapshot.ClientType != "mcpcli" || snapshot.RecentEvents[2].ClientType != "mcpcli" {
		t.Fatalf("short stateless handshake lost recognized client: %+v", snapshot)
	}
	store.record("owner-a", "tools/call", "machine_list", "other", "success", "", started.Add(mcpClientAttributionWindow+time.Second))
	if snapshot := store.snapshot("owner-a"); snapshot.ClientType != "other" {
		t.Fatalf("stale client attribution was retained: %+v", snapshot)
	}
}
