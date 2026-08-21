//go:build opse2e

package opsbackup

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestRestoredHubStartsHealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := t.TempDir()
	source := filepath.Join(base, "source")
	st, err := store.Open(ctx, filepath.Join(source, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := security.LoadOrCreateEd25519(filepath.Join(source, "secrets", "hub-ed25519.key")); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(base, "backup.zip")
	if _, err := Create(ctx, source, backupPath, "ops-e2e"); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(base, "restored")
	if _, err := Restore(ctx, backupPath, restored); err != nil {
		t.Fatal(err)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	hubBinary := filepath.Join(base, "fast-spider-hub")
	if runtime.GOOS == "windows" {
		hubBinary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", hubBinary, "./cmd/hub")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Hub: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	var logs bytes.Buffer
	hub := exec.CommandContext(ctx, hubBinary,
		"--listen", addr,
		"--data-dir", restored,
		"--allowed-hosts", "127.0.0.1,localhost",
		"--admin-password", "restored-hub-e2e-password-123",
	)
	hub.Stdout = &logs
	hub.Stderr = &logs
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hub.Process != nil {
			_ = hub.Process.Kill()
		}
		_ = hub.Wait()
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	for _, endpoint := range []string{"/livez", "/readyz"} {
		if err := waitForHTTP(ctx, client, "http://"+addr+endpoint); err != nil {
			t.Fatalf("%s did not become healthy: %v\nHub logs:\n%s", endpoint, err, logs.String())
		}
	}
}

func waitForHTTP(ctx context.Context, client *http.Client, url string) error {
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %s", response.Status)
		} else {
			lastErr = err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %v", ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}
