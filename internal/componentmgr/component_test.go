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
	"testing"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/security"
)

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
