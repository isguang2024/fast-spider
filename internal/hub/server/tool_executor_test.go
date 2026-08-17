package server

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTransportAdaptersUseSharedToolExecutor(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate tool executor test source")
	}
	dir := filepath.Dir(currentFile)
	for _, name := range []string{"mcp.go", "direct.go"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(".CallCapability(")) {
			t.Fatalf("%s bypasses shared toolExecutor with a direct CallCapability route", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tool_executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(".CallCapability(")) || !bytes.Contains(raw, []byte("func (e *toolExecutor) Execute(")) {
		t.Fatal("shared tool executor does not own capability routing")
	}
}
