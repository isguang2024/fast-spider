package browserext

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallArchiveSupportsSingleTopLevelDirectoryAndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "chatgpt.zip")
	writeExtensionArchive(t, archivePath, map[string]string{
		"ChatGPT/manifest.json": `{"manifest_version":3,"name":"ChatGPT","version":"1.2.3","background":{"service_worker":"background.js"}}`,
		"ChatGPT/background.js": "console.log('background');\n",
	})

	installed, err := InstallArchive(dataDir, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if installed.ID == "" || installed.Name != "ChatGPT" || installed.Version != "1.2.3" || installed.ManifestVersion != 3 || installed.Path == "" {
		t.Fatalf("installed=%+v", installed)
	}
	if !regularFile(filepath.Join(installed.Path, "manifest.json")) || !regularFile(filepath.Join(installed.Path, "background.js")) {
		t.Fatalf("normalized extension path is incomplete: %s", installed.Path)
	}
	listed, err := List(dataDir)
	if err != nil || len(listed) != 1 || listed[0].ID != installed.ID || listed[0].Path != installed.Path {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	resolved, err := Resolve(dataDir, installed.ID)
	if err != nil || resolved.Path != installed.Path {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	again, err := InstallArchive(dataDir, archivePath)
	if err != nil || again.Path != installed.Path {
		t.Fatalf("idempotent install=%+v err=%v", again, err)
	}
}

func TestInstallArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsafe.zip")
	writeExtensionArchive(t, archivePath, map[string]string{
		"../manifest.json": `{"manifest_version":3,"name":"Unsafe","version":"1.0.0"}`,
	})
	if _, err := InstallArchive(t.TempDir(), archivePath); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
}

func TestInstallArchiveRejectsUnsupportedManifest(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsupported.zip")
	writeExtensionArchive(t, archivePath, map[string]string{
		"manifest.json": `{"manifest_version":1,"name":"Old","version":"1.0.0"}`,
	})
	if _, err := InstallArchive(t.TempDir(), archivePath); err == nil {
		t.Fatal("unsupported manifest was accepted")
	}
}

func writeExtensionArchive(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, contents := range files {
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
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["ChatGPT/manifest.json"]), &manifest); err == nil && manifest == nil {
		t.Fatal("test manifest unexpectedly decoded as nil")
	}
}
