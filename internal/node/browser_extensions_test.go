package node

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/browserext"
)

func TestBrowserManagerResolvesInstalledExtensionsWithoutExposingPaths(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "extension.zip")
	writeNodeExtensionArchive(t, archivePath)
	installed, err := browserext.InstallArchive(dataDir, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewBrowserManager(dataDir, filepath.Join(t.TempDir(), "missing-sidecar"), nil)

	paths, ids, err := manager.extensionPaths(map[string]any{"extensionIds": []string{installed.ID}})
	if err != nil || len(paths) != 1 || len(ids) != 1 || ids[0] != installed.ID || paths[0] != installed.Path {
		t.Fatalf("paths=%v ids=%v err=%v", paths, ids, err)
	}
	result, err := manager.Execute(context.Background(), "extensions.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) == "null" || containsString(string(raw), installed.Path) {
		t.Fatalf("extension list leaked path or was empty: %s", raw)
	}
}

func TestBrowserManagerRejectsHeadlessExtensionLaunch(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "extension.zip")
	writeNodeExtensionArchive(t, archivePath)
	installed, err := browserext.InstallArchive(dataDir, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewBrowserManager(dataDir, filepath.Join(t.TempDir(), "missing-sidecar"), nil)
	_, err = manager.Execute(context.Background(), "launch", map[string]any{"headless": true, "extensionIds": []string{installed.ID}})
	var actionErr *BrowserActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "BROWSER_EXTENSION_REQUIRES_HEADED" {
		t.Fatalf("launch error=%v", err)
	}
}

func writeNodeExtensionArchive(t *testing.T, archivePath string) {
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

func containsString(value, needle string) bool {
	return needle != "" && len(value) >= len(needle) && stringContains(value, needle)
}

func stringContains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
