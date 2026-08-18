package core

import (
	"context"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

// AuditLogQuery is the public, owner-scoped query used by the MCP audit reader.
type AuditLogQuery struct {
	MachineID    string
	ActionPrefix string
	Result       string
	Before       time.Time
	Limit        int
}

func (s *Service) ListAuditLog(ctx context.Context, ownerID string, query AuditLogQuery) ([]store.AuditEntry, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, store.ErrUnauthorized
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	return s.store.ListAudit(ctx, store.AuditListQuery{
		OwnerID:      ownerID,
		MachineID:    query.MachineID,
		ActionPrefix: query.ActionPrefix,
		Result:       query.Result,
		Before:       query.Before,
		Limit:        limit,
	})
}
