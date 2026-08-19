package nodeui

import (
	"archive/zip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserExtensionInstallAPIImportsZIPAndHidesManagedPath(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "chatgpt.zip")
	writeNodeUIExtensionArchive(t, archivePath)
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}

	install := httptest.NewRecorder()
	app.handler().ServeHTTP(install, authorizedRequest(app, http.MethodPost, "/api/browser/extensions/install", mustJSON(t, browserExtensionInstallRequest{ArchivePath: archivePath})))
	if install.Code != http.StatusOK {
		t.Fatalf("install status=%d body=%s", install.Code, install.Body.String())
	}
	if strings.Contains(install.Body.String(), dataDir) || strings.Contains(install.Body.String(), archivePath) || strings.Contains(install.Body.String(), "path") {
		t.Fatalf("install response leaked managed path: %s", install.Body.String())
	}

	list := httptest.NewRecorder()
	app.handler().ServeHTTP(list, authorizedRequest(app, http.MethodGet, "/api/browser/extensions", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "ChatGPT") {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeNodeUIExtensionArchive(t *testing.T, archivePath string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, contents := range map[string]string{
		"ChatGPT/manifest.json": `{"manifest_version":3,"name":"ChatGPT","version":"1.0.0"}`,
		"ChatGPT/background.js": "console.log('extension');\n",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			_ = writer.Close()
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
