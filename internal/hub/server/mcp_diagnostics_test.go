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
	if len(a.RecentEvents) != maxMCPDiagnosticEvents || a.LastToolName != "file_read" || a.ClientType != "codex" || a.Result != "success" || a.Diagnosis != "no_initialize" {
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
