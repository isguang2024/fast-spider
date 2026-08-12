//go:build !windows

package node

import "context"

func prepareExecutionPlatform(_ context.Context, cwd string, argv []string, runtime executionRuntime) (string, []string, string, error) {
	if runtime.Kind == "wsl" {
		return "", nil, "", ErrRuntimeUnavailable
	}
	return cwd, append([]string(nil), argv...), "host", nil
}
