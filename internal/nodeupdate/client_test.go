package nodeupdate_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/nodeupdate"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestCheckAndStageSignedNodeUpdate(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("signed-node-update-binary")
	digest := sha256.Sum256(artifact)
	platform := nodeupdate.Platform()
	manifest := releaseinfo.NewManifest("node", "fast-spider-node", platform, "0.2.0", hex.EncodeToString(digest[:]), int64(len(artifact)), "/download")
	if err := releaseinfo.Sign(privateKey, &manifest); err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node/releases/" + platform + "/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(manifest)
		case "/download":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()

	ctx := context.Background()
	status, err := nodeupdate.Check(ctx, hub.URL, security.EncodePublicKey(publicKey), "0.1.9")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.LatestVersion != "0.2.0" {
		t.Fatalf("unexpected update status: %+v", status)
	}
	dataDir := t.TempDir()
	stagedStatus, artifactPath, err := nodeupdate.Stage(ctx, dataDir, hub.URL, security.EncodePublicKey(publicKey), "0.1.9", status)
	if err != nil {
		t.Fatal(err)
	}
	if !stagedStatus.Ready || artifactPath == "" {
		t.Fatalf("update was not staged: status=%+v path=%q", stagedStatus, artifactPath)
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(artifact) {
		t.Fatalf("staged artifact mismatch: %q", raw)
	}
	ready, readyPath, err := nodeupdate.Ready(dataDir, security.EncodePublicKey(publicKey), "0.1.9")
	if err != nil || !ready.Ready || filepath.Clean(readyPath) != filepath.Clean(artifactPath) {
		t.Fatalf("ready status=%+v path=%q err=%v", ready, readyPath, err)
	}
}

func TestCheckRejectsManifestSignedByDifferentHub(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, attackerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("malicious")
	digest := sha256.Sum256(artifact)
	platform := nodeupdate.Platform()
	manifest := releaseinfo.NewManifest("node", "fast-spider-node", platform, "9.9.9", hex.EncodeToString(digest[:]), int64(len(artifact)), "/download")
	if err := releaseinfo.Sign(attackerPrivate, &manifest); err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer hub.Close()
	if _, err := nodeupdate.Check(context.Background(), hub.URL, security.EncodePublicKey(publicKey), "0.1.0"); err == nil {
		t.Fatal("update signed by a different Hub key was accepted")
	}
}
