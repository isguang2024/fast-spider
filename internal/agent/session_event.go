package agent

type AgentEvent struct {
	Sequence    int64          `json:"sequence"`
	Type        string         `json:"type"`
	SessionID   string         `json:"sessionId,omitempty"`
	TurnID      string         `json:"turnId,omitempty"`
	RequestID   string         `json:"requestId,omitempty"`
	RequestType string         `json:"requestType,omitempty"`
	Text        string         `json:"text,omitempty"`
	State       string         `json:"state,omitempty"`
	Detail      map[string]any `json:"detail,omitempty"`
	Timestamp   string         `json:"timestamp"`
}
