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

func TestPublishPresentationFileUsesAttachmentHubRelay(t *testing.T) {
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
			if r.Header.Get("Content-Type") != "image/png" || r.Header.Get("X-Fast-Spider-SHA256") != wantSHA || r.Header.Get("X-Fast-Spider-Resource-Kind") != attachmentResourceKind {
				t.Fatalf("presentation headers contentType=%q sha=%q kind=%q", r.Header.Get("Content-Type"), r.Header.Get("X-Fast-Spider-SHA256"), r.Header.Get("X-Fast-Spider-Resource-Kind"))
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
	result, err := client.publishPresentationFile(context.Background(), filePath, "截图.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if result["presentationId"] != "prs_test" || result["sha256"] != wantSHA || result["fileName"] != "截图.png" || result["contentType"] != "image/png" {
		t.Fatalf("presentation result=%+v", result)
	}
}

func TestPresentationPublishFileUsesAttachmentResourceKind(t *testing.T) {
	payload := []byte("attachment-payload")
	sum := sha256.Sum256(payload)
	wantSHA := "sha256:" + hex.EncodeToString(sum[:])
	expiresAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/device/token":
			writePresentationTestJSON(t, w, map[string]any{"deviceToken": "dev_test", "expiresAt": time.Now().UTC().Add(time.Hour)})
		case "/node/v1/presentations":
			if r.Header.Get("X-Fast-Spider-Resource-Kind") != attachmentResourceKind {
				t.Fatalf("attachment resource kind=%q", r.Header.Get("X-Fast-Spider-Resource-Kind"))
			}
			nameRaw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Fast-Spider-File-Name"))
			if err != nil || string(nameRaw) != "attachment.png" {
				t.Fatalf("attachment file name=%q err=%v", string(nameRaw), err)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != string(payload) {
				t.Fatalf("attachment body=%q err=%v", string(body), err)
			}
			w.WriteHeader(http.StatusCreated)
			writePresentationTestJSON(t, w, map[string]any{
				"presentationId": "prs_attachment", "fileName": "attachment.png", "contentType": "image/png",
				"sizeBytes": len(payload), "sha256": wantSHA, "expiresAt": expiresAt, "resourceKind": attachmentResourceKind,
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
	if err := SaveState(client.statePath, State{HubURL: server.URL, MachineID: "mach_test", HubPublicKey: "pub_test", HubFingerprint: "fp_test"}); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dataDir, "source.png")
	if err := os.WriteFile(filePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.presentationPublishFile(context.Background(), map[string]any{"path": filePath, "logicalName": "attachment.png", "contentType": "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if result["presentationId"] != "prs_attachment" || result["fileName"] != "attachment.png" {
		t.Fatalf("attachment result=%+v", result)
	}
}

func writePresentationTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
