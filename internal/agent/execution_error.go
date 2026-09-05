package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/isguang2024/fast-spider/internal/node"
)

type ErrorClass string

const (
	ErrorAuthFailed          ErrorClass = "auth_failed"
	ErrorRateLimited         ErrorClass = "rate_limited"
	ErrorProviderUnavailable ErrorClass = "provider_unavailable"
	ErrorNetworkFailed       ErrorClass = "network_failed"
	ErrorInvalidModel        ErrorClass = "invalid_model"
	ErrorRuntimeUnavailable  ErrorClass = "runtime_unavailable"
	ErrorRouteMismatch       ErrorClass = "route_mismatch"
	ErrorConfigInvalid       ErrorClass = "config_invalid"
	ErrorUnknown             ErrorClass = "unknown"
)

type ExecutionError struct {
	Class     ErrorClass
	Provider  string
	Operation string
	debugText string
}

func (e *ExecutionError) Error() string { return publicErrorMessage(e.Class) }

func classifyExecutionError(err error) ErrorClass {
	if err == nil {
		return ErrorUnknown
	}
	var classified *ExecutionError
	if errors.As(err, &classified) {
		return classified.Class
	}
	if errors.Is(err, errCodexDesktopIPCProtocol) {
		return ErrorConfigInvalid
	}
	if errors.Is(err, errCodexDesktopOwnerUnavailable) {
		return ErrorRuntimeUnavailable
	}
	if errors.Is(err, node.ErrAgentProviderUnavailable) {
		if classifyExecutionText(err.Error()) == ErrorRuntimeUnavailable {
			return ErrorRuntimeUnavailable
		}
		return ErrorProviderUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorNetworkFailed
	}
	return classifyExecutionText(err.Error())
}

func classifyExecutionText(raw string) ErrorClass {
	text := strings.ToLower(raw)
	switch {
	case containsAny(text, "failed to load configuration", "failed to resolve feature override precedence", "invalid configuration"):
		return ErrorConfigInvalid
	case containsAny(text, "route mismatch", "selection inconsistent", "provider drift", "wrong upstream", "route_mismatch"):
		return ErrorRouteMismatch
	case containsAny(text, "invalid model", "model_not_found", "model not found", "unknown model", "unsupported model"):
		return ErrorInvalidModel
	case containsAny(text, "rate limit", "rate_limit", "too many requests", "quota exceeded", "status 429", "http 429"):
		return ErrorRateLimited
	case containsAny(text, "unauthorized", "unauthenticated", "authentication failed", "auth failed", "not logged in", "invalid api key", "expired credential", "status 401", "http 401"):
		return ErrorAuthFailed
	case containsAny(text, "executable not found", "runtime unavailable", "failed to start", "spawn failed", "app-server unavailable"):
		return ErrorRuntimeUnavailable
	case containsAny(text, "provider unavailable", "upstream unavailable", "service unavailable", "status 503", "http 503"):
		return ErrorProviderUnavailable
	case containsAny(text, "connection refused", "connection reset", "network", "dns", "tls", "timed out", "timeout", "eof"):
		return ErrorNetworkFailed
	default:
		return ErrorUnknown
	}
}

func newExecutionError(provider, operation, debugText string) *ExecutionError {
	return &ExecutionError{Class: classifyExecutionText(debugText), Provider: provider, Operation: operation, debugText: debugText}
}

func wrapExecutionError(provider, operation string, err error) *ExecutionError {
	return &ExecutionError{Class: classifyExecutionError(err), Provider: provider, Operation: operation, debugText: executionDebugText(err)}
}

func executionDebugText(err error) string {
	var classified *ExecutionError
	if errors.As(err, &classified) {
		return classified.debugText
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func publicErrorMessage(class ErrorClass) string {
	switch class {
	case ErrorAuthFailed:
		return "AI provider authentication failed"
	case ErrorRateLimited:
		return "AI provider rate limit was reached"
	case ErrorProviderUnavailable:
		return "AI provider is unavailable"
	case ErrorNetworkFailed:
		return "AI provider network request failed"
	case ErrorInvalidModel:
		return "the selected AI model is unavailable"
	case ErrorRuntimeUnavailable:
		return "AI runtime is unavailable"
	case ErrorRouteMismatch:
		return "AI route selection does not match the active upstream"
	case ErrorConfigInvalid:
		return "AI runtime configuration is incompatible"
	default:
		return "AI execution failed"
	}
}

func validErrorClass(class ErrorClass) bool {
	switch class {
	case ErrorAuthFailed, ErrorRateLimited, ErrorProviderUnavailable, ErrorNetworkFailed,
		ErrorInvalidModel, ErrorRuntimeUnavailable, ErrorRouteMismatch, ErrorConfigInvalid, ErrorUnknown:
		return true
	default:
		return false
	}
}

func (e *ExecutionError) CapabilityError() (string, string, bool) {
	if e == nil {
		return "AGENT_EXECUTION_FAILED", publicErrorMessage(ErrorUnknown), false
	}
	if e.Class == ErrorConfigInvalid {
		return "AGENT_CONFIG_INVALID", publicErrorMessage(e.Class), false
	}
	return "AGENT_EXECUTION_FAILED", publicErrorMessage(e.Class), e.Class == ErrorNetworkFailed || e.Class == ErrorRateLimited || e.Class == ErrorProviderUnavailable
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
