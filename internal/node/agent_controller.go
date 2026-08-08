package node

import (
	"context"
	"errors"
)

// AgentController is the narrow provider boundary used by the Node capability
// dispatcher. The provider implementation lives outside this package.
type AgentController interface {
	Control(context.Context, string, string, map[string]any) (map[string]any, error)
	Close(context.Context) error
}

var (
	ErrAgentProviderUnavailable = errors.New("agent provider unavailable")
	ErrAgentSessionNotFound     = errors.New("agent session not found")
	ErrAgentSessionBusy         = errors.New("agent session already has an active run")
)
