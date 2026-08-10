package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadPresentationFileUsesDirectHubRelay(t *testing.T) {
	payload := []byte("presentation-payload")
	sum := sha256.Sum256(payload)
	wantSHA := "sha256:" + hex.EncodeToString(sum[:])
	expiresAt := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/device/token":
			writePresentationTestJSON(t, w, map[string]any{
				"deviceToken": "dev_test",
				"expiresAt":   time.Now().UTC().Add(time.Hour),
			})
		case "/node/v1/presentations":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer dev_test" {
				t.Fatalf("unexpected presentation request method=%s auth=%q", r.Method, r.Header.Get("Authorization"))
			}
			nameRaw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Fast-Spider-File-Name"))
			if err != nil || string(nameRaw) != "截图.png" {
				t.Fatalf("presentation file name=%q err=%v", string(nameRaw), err)
			}
			if r.Header.Get("Content-Type") != "image/png" || r.Header.Get("X-Fast-Spider-SHA256") != wantSHA {
				t.Fatalf("presentation headers contentType=%q sha=%q", r.Header.Get("Content-Type"), r.Header.Get("X-Fast-Spider-SHA256"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != string(payload) || r.ContentLength != int64(len(payload)) {
				t.Fatalf("presentation body=%q length=%d err=%v", string(body), r.ContentLength, err)
			}
			w.WriteHeader(http.StatusCreated)
			writePresentationTestJSON(t, w, map[string]any{
				"presentationId": "prs_test",
				"fileName":       "截图.png",
				"contentType":    "image/png",
				"sizeBytes":      len(payload),
				"sha256":         wantSHA,
				"expiresAt":      expiresAt,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveState(client.statePath, State{
		HubURL: server.URL, MachineID: "mach_test", HubPublicKey: "pub_test", HubFingerprint: "fp_test",
	}); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dataDir, "source.png")
	if err := os.WriteFile(filePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.uploadPresentationFile(context.Background(), filePath, "截图.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if result.PresentationID != "prs_test" || result.SHA256 != wantSHA || result.FileName != "截图.png" || result.ContentType != "image/png" {
		t.Fatalf("presentation result=%+v", result)
	}
}

func writePresentationTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
