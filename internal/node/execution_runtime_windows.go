//go:build windows

package node

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func prepareExecutionPlatform(ctx context.Context, cwd string, argv []string, runtime executionRuntime) (string, []string, string, error) {
	if runtime.Kind == "host" {
		return cwd, append([]string(nil), argv...), "host", nil
	}
	if len(argv) > 0 {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(argv[0])))
		if base == "wsl" || base == "wsl.exe" {
			return "", nil, "", fmt.Errorf("runtime=wsl argv must name a Linux executable")
		}
	}
	prefix := []string{}
	if runtime.Distribution != "" {
		prefix = append(prefix, "--distribution", runtime.Distribution)
	}
	mapArgs := append(append([]string{}, prefix...), "--exec", "wslpath", "-u", "--", cwd)
	command := exec.CommandContext(ctx, "wsl.exe", mapArgs...)
	command.Env = safeShellEnvironment()
	stdout := &boundedCommandBuffer{limit: 8 << 10}
	command.Stdout = stdout
	command.Stderr = &boundedCommandBuffer{limit: 8 << 10}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", nil, "", ctx.Err()
		}
		return "", nil, "", ErrWSLCwdUnmappable
	}
	mapped := strings.TrimSpace(string(bytes.TrimSpace(stdout.Bytes())))
	if mapped == "" || !strings.HasPrefix(mapped, "/") || strings.ContainsAny(mapped, "\x00\r\n") {
		return "", nil, "", ErrWSLCwdUnmappable
	}
	wslArgs := append(append([]string{}, prefix...), "--cd", mapped, "--exec")
	wslArgs = append(wslArgs, argv...)
	return cwd, append([]string{"wsl.exe"}, wslArgs...), "wsl", nil
}
