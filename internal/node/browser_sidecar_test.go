package node

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
