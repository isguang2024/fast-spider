//go:build codexowner_e2e

package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

// TestCodexAdapterExternalAppServerOwnerE2E starts an app-server in a
// temporary CODEX_HOME, then uses two Fast Spider adapters through the same
// app-server proxy. It intentionally never touches the user's live Codex
// state; it is the opt-in proof for the shared-owner transport only.
func TestCodexAdapterExternalAppServerOwnerE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_OWNER_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_OWNER_E2E=1 to run the temporary shared-owner Codex test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	candidates := codexExecutableCandidates()
	if len(candidates) == 0 {
		t.Skip("Codex executable unavailable")
	}
	executable := candidates[0]
	rootBase := filepath.Join(strings.TrimSpace(os.Getenv("LOCALAPPDATA")), "Temp")
	if !filepath.IsAbs(rootBase) {
		rootBase = t.TempDir()
	}
	root, err := os.MkdirTemp(rootBase, "fast-spider-codex-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	codexHome := filepath.Join(root, "codex-home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "app-server.sock")

	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAST_SPIDER_CODEX_EXECUTABLE", executable)
	t.Setenv(codexAppServerSocketEnv, socketPath)

	server := exec.Command(executable, "app-server", "--listen", "unix://"+socketPath)
	server.Dir = codexHome
	server.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	var serverOutput bytes.Buffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	node.ConfigureProcessTree(server)
	if err := server.Start(); err != nil {
		t.Fatalf("start temporary Codex app-server: %v", err)
	}
	t.Cleanup(func() {
		if server.Process != nil && server.ProcessState == nil {
			_ = node.KillProcessTree(server)
		}
		_ = server.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if server.ProcessState != nil {
			t.Fatalf("temporary Codex app-server exited before socket became ready")
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary Codex app-server socket did not become ready: %s; output=%s", socketPath, serverOutput.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	first := NewCodexAdapter(nil)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = first.Close(closeCtx)
	}()
	if _, err := first.Availability(ctx); err != nil {
		t.Fatalf("temporary Codex availability: %v", err)
	}
	threadResult, err := first.StartThread(ctx, workspace, workspace, "", "")
	if err != nil {
		t.Fatalf("thread/start through external app-server proxy: %v", err)
	}
	sessionID := mapNestedString(threadResult, "thread", "id")
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("thread/start returned no session ID: %#v", threadResult)
	}
	if _, err := first.ReadThread(ctx, sessionID); err != nil {
		t.Fatalf("thread/read before shared-owner archive: %v (%s)", err, executionDebugText(err))
	}
	turnResult, turnErr := first.StartTurn(ctx, sessionID, "只回复 OWNER_E2E，不调用任何工具。", workspace, "", "")
	t.Logf("temporary owner turn result=%#v err=%v", turnResult, turnErr)
	time.Sleep(2 * time.Second)

	second := NewCodexAdapter(nil)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = second.Close(closeCtx)
	}()
	if err := second.ensureThreadLoaded(ctx, sessionID); err != nil {
		t.Fatalf("thread/resume through second proxy before archive: %v (%s)", err, executionDebugText(err))
	}
	if err := second.ArchiveThread(ctx, sessionID); err != nil {
		t.Fatalf("thread/archive through second proxy on shared app-server: %v (%s)", err, executionDebugText(err))
	}
	if _, err := first.ReadThread(ctx, sessionID); err != nil {
		t.Fatalf("thread/read after shared-owner archive: %v", err)
	}
}
