package node

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestArtifactCompleteRetryDoesNotReuploadChunk(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	workspaceStore := NewWorkspaceStore(dataDir)
	workspace, err := workspaceStore.Add(root, "artifact-retry")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("artifact retry payload")
	if err := os.WriteFile(filepath.Join(root, "payload.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	createCalls := 0
	chunkCalls := 0
	completeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/device/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceToken": "dev_artifact_retry"})
		case r.Method == http.MethodPost && r.URL.Path == "/node/v1/artifacts":
			mu.Lock()
			createCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artifactId": "art_retry", "uploadId": "upl_retry", "chunkBytes": int64(len(payload)),
				"receivedBytes": 0,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/node/v1/artifacts/upl_retry/chunk":
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil || string(body) != string(payload) || r.URL.Query().Get("offset") != "0" {
				http.Error(w, "invalid chunk", http.StatusBadRequest)
				return
			}
			mu.Lock()
			chunkCalls++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/node/v1/artifacts/upl_retry/complete":
			mu.Lock()
			completeCalls++
			attempt := completeCalls
			mu.Unlock()
			if attempt == 1 {
				http.Error(w, "temporary complete failure", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	if err := SaveState(client.statePath, State{HubURL: server.URL, MachineID: "mach_artifact_retry", CredentialID: "cred_artifact_retry", HubPublicKey: "hub-key", HubFingerprint: "hub-fingerprint"}); err != nil {
		t.Fatal(err)
	}

	result, err := client.artifactUploadFile(context.Background(), workspace.WorkspaceID, map[string]any{"path": "payload.txt", "logicalName": "payload.txt", "contentType": "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactID != "art_retry" {
		t.Fatalf("artifact result=%+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if createCalls != 1 || chunkCalls != 1 || completeCalls != 2 {
		t.Fatalf("artifact request counts create=%d chunk=%d complete=%d", createCalls, chunkCalls, completeCalls)
	}
}
