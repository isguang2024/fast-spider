package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestSignedNodeReleaseManifestAndDownload(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	releaseDir := filepath.Join(service.ReleaseDir(), "node", "windows-amd64")
	if err := os.MkdirAll(releaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("fake-windows-node-release")
	if err := os.WriteFile(filepath.Join(releaseDir, "fast-spider-node.exe"), artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "version.txt"), []byte("0.2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hub := httptest.NewServer(server.New(service, server.Config{}).Handler())
	defer hub.Close()
	resp, err := http.Get(hub.URL + "/api/v1/node/releases/windows-amd64/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d", resp.StatusCode)
	}
	var manifest releaseinfo.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	publicKey, err := security.DecodePublicKey(service.HubPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseinfo.Verify(publicKey, manifest); err != nil {
		t.Fatalf("release manifest signature: %v", err)
	}
	if manifest.Version != "0.2.0" || manifest.Kind != "node" || manifest.ID != "fast-spider-node" || manifest.Platform != "windows-amd64" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	download, err := http.Get(hub.URL + manifest.DownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	raw, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatal(err)
	}
	if download.StatusCode != http.StatusOK || string(raw) != string(artifact) {
		t.Fatalf("download status=%d body=%q", download.StatusCode, raw)
	}
}
