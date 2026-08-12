package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrRuntimeUnavailable = errors.New("execution runtime unavailable")
	ErrWSLCwdUnmappable   = errors.New("WSL cwd cannot be mapped")
)

type executionRuntime struct {
	Kind         string `json:"kind,omitempty"`
	Distribution string `json:"distribution,omitempty"`
}

func normalizeExecutionRuntime(runtime executionRuntime) (executionRuntime, error) {
	runtime.Kind = strings.ToLower(strings.TrimSpace(runtime.Kind))
	runtime.Distribution = strings.TrimSpace(runtime.Distribution)
	if runtime.Kind == "" {
		runtime.Kind = "host"
	}
	if runtime.Kind != "host" && runtime.Kind != "wsl" {
		return executionRuntime{}, fmt.Errorf("runtime.kind must be host or wsl")
	}
	if runtime.Kind == "host" && runtime.Distribution != "" {
		return executionRuntime{}, fmt.Errorf("runtime.distribution is only valid for wsl")
	}
	if len(runtime.Distribution) > 128 || strings.ContainsAny(runtime.Distribution, "\x00\r\n\t") {
		return executionRuntime{}, fmt.Errorf("runtime.distribution is invalid")
	}
	return runtime, nil
}

func prepareExecution(ctx context.Context, cwd string, argv []string, runtime executionRuntime) (string, []string, string, error) {
	normalized, err := normalizeExecutionRuntime(runtime)
	if err != nil {
		return "", nil, "", err
	}
	return prepareExecutionPlatform(ctx, cwd, argv, normalized)
}
