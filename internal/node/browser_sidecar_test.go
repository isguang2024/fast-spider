package node

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrowserSidecarRejectsLegacyPolicyProtocol(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"fastSpider":{"sidecarProtocol":"1.0"}}`,
		"index.mjs":    "export {};\n",
		filepath.Join("node_modules", "playwright", "package.json"): `{"name":"playwright"}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewBrowserSidecar(dir, nil).Available(); !errors.Is(err, ErrBrowserUnavailable) || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("legacy sidecar error=%v", err)
	}
}

func TestBrowserSidecarUsesBundledNodeAndBrowserCache(t *testing.T) {
	dir := t.TempDir()
	nodeName := "node"
	if runtime.GOOS == "windows" {
		nodeName = "node.exe"
	}
	nodePath := filepath.Join(dir, nodeName)
	if err := os.WriteFile(nodePath, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	browserRoot := filepath.Join(dir, "browsers")
	if err := os.MkdirAll(browserRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	sidecar := &BrowserSidecar{dir: dir}
	resolved, err := sidecar.nodeExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(resolved) != filepath.Clean(nodePath) {
		t.Fatalf("node executable=%q want=%q", resolved, nodePath)
	}

	want := "PLAYWRIGHT_BROWSERS_PATH=" + browserRoot
	found := false
	for _, item := range browserSidecarEnvironment(dir) {
		if strings.EqualFold(item, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("browser environment omitted %q", want)
	}
}
