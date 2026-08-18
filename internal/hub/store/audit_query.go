package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// AuditListQuery describes a bounded, owner-scoped audit read.
type AuditListQuery struct {
	OwnerID      string
	MachineID    string
	ActionPrefix string
	Result       string
	Before       time.Time
	Limit        int
}

// ListAudit returns audit entries for exactly one owner, newest first.
// Callers must provide a non-empty OwnerID; this method never performs an
// unscoped audit scan.
func (s *Store) ListAudit(ctx context.Context, query AuditListQuery) ([]AuditEntry, error) {
	ownerID := strings.TrimSpace(query.OwnerID)
	if ownerID == "" {
		return nil, ErrUnauthorized
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	where := []string{"owner_id = ?"}
	args := []any{ownerID}
	if machineID := strings.TrimSpace(query.MachineID); machineID != "" {
		where = append(where, "machine_id = ?")
		args = append(args, machineID)
	}
	if prefix := strings.TrimSpace(query.ActionPrefix); prefix != "" {
		where = append(where, "substr(action, 1, length(?)) = ?")
		args = append(args, prefix, prefix)
	}
	if result := strings.TrimSpace(query.Result); result != "" {
		where = append(where, "result = ?")
		args = append(args, result)
	}
	if !query.Before.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, query.Before.UTC().Unix())
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `SELECT
		id, owner_id, machine_id, actor_type, actor_id, action, result, remote_addr, detail_json, created_at
		FROM audit_entries
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var (
			entry                         AuditEntry
			owner, machine, actor, remote sql.NullString
			detail                        sql.NullString
			createdAt                     int64
		)
		if err := rows.Scan(&entry.ID, &owner, &machine, &entry.ActorType, &actor, &entry.Action, &entry.Result, &remote, &detail, &createdAt); err != nil {
			return nil, err
		}
		entry.OwnerID = owner.String
		entry.MachineID = machine.String
		entry.ActorID = actor.String
		entry.RemoteAddr = remote.String
		entry.CreatedAt = time.Unix(createdAt, 0).UTC()
		if detail.Valid && strings.TrimSpace(detail.String) != "" {
			var decoded any
			if err := json.Unmarshal([]byte(detail.String), &decoded); err != nil {
				return nil, err
			}
			entry.Detail = decoded
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
