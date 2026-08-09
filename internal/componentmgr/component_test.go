package componentmgr_test

import (
	"archive/zip"
	"bytes"
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
	"runtime"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestCleanupConfiguredUsesManagedInstalledManifest(t *testing.T) {
	dataDir := t.TempDir()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	currentDir := filepath.Join(dataDir, "components", "browser", "2.0.0")
	if err := os.MkdirAll(currentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := releaseinfo.NewManifest("component", "browser", platform, "2.0.0", strings.Repeat("a", 64), 1, "/component-download")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentDir, ".fast-spider-component.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(dataDir, "components", "browser", "1.0.0")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dataDir, "cache", "components", "browser-"+platform+"-2.0.0.zip")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := componentmgr.CleanupConfigured(dataDir, "browser", currentDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("configured component was removed: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old configured component version remains: %v", err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("configured component cache remains: %v", err)
	}
}

func TestCleanupInstalledRemovesComponentCacheAndOlderVersions(t *testing.T) {
	dataDir := t.TempDir()
	installed := componentmgr.Installed{ID: "browser", Platform: runtime.GOOS + "-" + runtime.GOARCH, Version: "2.0.0"}
	installed.Path = filepath.Join(dataDir, "components", installed.ID, installed.Version)
	for _, path := range []string{
		filepath.Join(installed.Path, "current.txt"),
		filepath.Join(dataDir, "components", installed.ID, "1.0.0", "old.txt"),
		filepath.Join(dataDir, "cache", "components", installed.ID+"-"+installed.Platform+"-1.0.0.zip"),
		filepath.Join(dataDir, "cache", "components", installed.ID+"-"+installed.Platform+"-2.0.0.zip"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := componentmgr.CleanupInstalled(dataDir, installed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installed.Path, "current.txt")); err != nil {
		t.Fatalf("current component was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "components", installed.ID, "1.0.0")); !os.IsNotExist(err) {
		t.Fatalf("old component version remains: %v", err)
	}
	cacheDir := filepath.Join(dataDir, "cache", "components")
	if entries, err := os.ReadDir(cacheDir); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("component cache still contains %d files", len(entries))
	}
}

func TestEnsureDownloadsSignedComponentIntoManagedDirectory(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("sidecar/browser.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("console.log('browser');\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive.Bytes())
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifest := releaseinfo.NewManifest("component", "browser", platform, "1.0.0", hex.EncodeToString(digest[:]), int64(archive.Len()), "/component-download")
	if err := releaseinfo.Sign(privateKey, &manifest); err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node/components/browser/" + platform + "/latest":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/component-download":
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	dataDir := t.TempDir()
	installed, err := componentmgr.Ensure(context.Background(), dataDir, hub.URL, security.EncodePublicKey(publicKey), "browser")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Version != "1.0.0" || filepath.Clean(installed.Path) != filepath.Clean(filepath.Join(dataDir, "components", "browser", "1.0.0")) {
		t.Fatalf("unexpected installed component: %+v", installed)
	}
	raw, err := os.ReadFile(filepath.Join(installed.Path, "sidecar", "browser.js"))
	if err != nil || !bytes.Contains(raw, []byte("browser")) {
		t.Fatalf("component file missing: raw=%q err=%v", raw, err)
	}
}
