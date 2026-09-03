package node

import (
	"context"
	"log/slog"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

// NewLocalCapabilityClient creates an in-process capability client without
// loading or creating Node identity keys. It is intended for bounded local
// diagnostics that do not connect to a Hub.
func NewLocalCapabilityClient(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	client := &Client{
		cfg:           cfg,
		jobs:          NewJobManager(cfg.DataDir),
		requestSem:    make(chan struct{}, 8),
		screenshotSem: make(chan struct{}, 1),
		agent:         cfg.Agent,
		operationLog:  cfg.OperationLog,
	}
	client.browser = NewBrowserManager(cfg.DataDir, cfg.BrowserSidecarDir, cfg.Logger)
	if setter, ok := cfg.Agent.(interface{ SetCloudResultPublisher(any) }); ok {
		setter.SetCloudResultPublisher(client)
	}
	return client
}

// HandleLocalCapability is the lightweight adapter used by the standalone
// Local Bridge transport.
func (c *Client) HandleLocalCapability(ctx context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
	return c.handleCapabilityRequest(ctx, req)
}
