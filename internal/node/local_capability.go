package node

import (
	"context"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

// HandleLocalCapability is the lightweight adapter used by the standalone
// Local Bridge transport.
func (c *Client) HandleLocalCapability(ctx context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
	return c.handleCapabilityRequest(ctx, req)
}
