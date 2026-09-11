package node

import (
	"errors"

	"github.com/isguang2024/fast-spider/hostapi"
)

// AgentController remains as an internal alias for source compatibility. The
// cross-module contract lives in hostapi so a specialized module never imports
// an internal package.
type AgentController = hostapi.AgentController

type AgentCapabilityError = hostapi.CapabilityError

var (
	ErrAgentProviderUnavailable = errors.New("agent provider unavailable")
	ErrAgentSessionNotFound     = errors.New("agent session not found")
	ErrAgentSessionBusy         = errors.New("agent session already has an active run")
)
