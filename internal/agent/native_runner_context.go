package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	nativeEvidenceDefaultLimit = 10
	nativeEvidenceMaxLimit     = 50
	nativeEvidenceTextDefault  = 4096
	nativeEvidenceTextMax      = 16384
)

// initNativeEvidenceSchema creates the bounded evidence index. The runner
// ledger remains the execution source of truth; this table is query-only.
func initNativeEvidenceSchema(db *sql.DB) error {
	if db == nil {
		return errors.New("native evidence database is nil")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS runner_evidence (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        project_id TEXT NOT NULL,
        task_id TEXT NOT NULL,
        round INTEGER NOT NULL,
        event_id TEXT NOT NULL,
        kind TEXT NOT NULL,
        status TEXT NOT NULL,
        summary TEXT NOT NULL,
        source_path TEXT,
        sha256 TEXT,
        bytes INTEGER NOT NULL,
        content BLOB NOT NULL,
        created INTEGER NOT NULL,
        UNIQUE(project_id,task_id,round,event_id)
    );
    CREATE INDEX IF NOT EXISTS runner_evidence_project_task ON runner_evidence(project_id,task_id,id);
    CREATE INDEX IF NOT EXISTS runner_evidence_project_created ON runner_evidence(project_id,created);`)
	return err
}

func (r *nativeRunner) indexEvidence(ctx context.Context, task nativeRunnerTask, result nativeRunnerResult, raw []byte) error {
	if r == nil || r.db == nil {
		return errors.New("native evidence database is unavailable")
	}
	if strings.TrimSpace(task.ProjectID) == "" || strings.TrimSpace(task.ID) == "" || task.Round < 1 || strings.TrimSpace(result.EventID) == "" {
		return errors.New("evidence requires exact project, task, round and event identity")
	}
	if result.Path != "" {
		info, err := os.Stat(result.Path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("evidence source path is unavailable")
		}
	}
	digest := sha256.Sum256(raw)
	actual := hex.EncodeToString(digest[:])
	if result.SHA256 != "" && strings.TrimPrefix(result.SHA256, "sha256:") != actual {
		return errors.New("evidence content digest does not match result")
	}
	status := result.Outcome
	if status == "" {
		status = "unknown"
	}
	kind := "report"
	if task.Kind == "planner" {
		kind = "plan"
	}
	if result.ErrorCode != "" || result.Outcome == "blocked" {
		kind = "recovery"
	}
	var existing string
	if err := r.db.QueryRowContext(ctx, `SELECT sha256 FROM runner_evidence WHERE project_id=? AND task_id=? AND round=? AND event_id=?`, task.ProjectID, task.ID, task.Round, result.EventID).Scan(&existing); err == nil {
		if existing != actual {
			return errors.New("evidence event identity conflicts with stored content")
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO runner_evidence
        (project_id,task_id,round,event_id,kind,status,summary,source_path,sha256,bytes,content,created)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now'))`, task.ProjectID, task.ID, task.Round, result.EventID, kind, status, result.Summary, result.Path, actual, len(raw), raw)
	return err
}

func (r *nativeRunner) QueryContext(ctx context.Context, params map[string]any) (map[string]any, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("native runner database is unavailable")
	}
	p, err := decodeNativeContextParams(params)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		return nil, errors.New("projectId is required")
	}
	taskID := strings.TrimSpace(p.TaskID)
	section := strings.ToLower(strings.TrimSpace(p.Section))
	if section == "" {
		section = "tasks"
	}
	if section != "tasks" && section != "task" && section != "history" && section != "evidence" {
		return nil, errors.New("section must be tasks, task, history, or evidence")
	}
	limit, offset := nativeEvidenceDefaultLimit, 0
	if p.Limit != nil {
		limit = *p.Limit
	}
	if p.Offset != nil {
		offset = *p.Offset
	}
	if section != "evidence" && (limit < 1 || limit > nativeEvidenceMaxLimit || offset < 0) {
		return nil, errors.New("limit or offset is outside the allowed range")
	}
	if section == "evidence" && p.EvidenceID == nil && (limit < 1 || limit > nativeEvidenceMaxLimit || offset < 0) {
		return nil, errors.New("limit or offset is outside the allowed range")
	}
	if taskID != "" {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM runner_tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("task does not belong to project")
			}
			return nil, err
		}
	}
	out := map[string]any{"projectId": projectID, "section": section, "limit": limit, "offset": offset}
	switch section {
	case "tasks":
		items, err := r.queryTaskIndex(ctx, projectID, taskID, limit, offset)
		if err != nil {
			return nil, err
		}
		out["tasks"] = items
		out["nextOffset"] = offset + len(items)
		out["hasMore"] = len(items) == limit
	case "task":
		item, err := r.queryTask(ctx, projectID, taskID)
		if err != nil {
			return nil, err
		}
		if len(p.Fields) > 0 {
			item, err = selectTaskFields(item, p.Fields)
			if err != nil {
				return nil, err
			}
		}
		out["task"] = item
	case "history":
		items, err := r.queryHistory(ctx, projectID, taskID, limit, offset)
		if err != nil {
			return nil, err
		}
		out["history"] = items
		out["nextOffset"] = offset + len(items)
		out["hasMore"] = len(items) == limit
	case "evidence":
		if p.EvidenceID != nil {
			contentLimit := nativeEvidenceTextDefault
			if p.Limit != nil {
				contentLimit = *p.Limit
			}
			if contentLimit < 1 || contentLimit > nativeEvidenceTextMax || offset < 0 {
				return nil, errors.New("evidence content limit or offset is outside the allowed range")
			}
			item, err := r.queryEvidenceContent(ctx, projectID, taskID, *p.EvidenceID, offset, contentLimit)
			if err != nil {
				return nil, err
			}
			out["evidence"] = item
		} else {
			if taskID != "" {
				if err := r.lazyIndexTaskEvidence(ctx, projectID, taskID, p.Round); err != nil {
					return nil, err
				}
			}
			items, err := r.queryEvidenceIndex(ctx, projectID, taskID, limit, offset)
			if err != nil {
				return nil, err
			}
			out["evidence"] = items
			out["nextOffset"] = offset + len(items)
			out["hasMore"] = len(items) == limit
		}
	}
	return out, nil
}

type nativeContextParams struct {
	ProjectID  string   `json:"projectId"`
	TaskID     string   `json:"taskId,omitempty"`
	Section    string   `json:"section,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
	Offset     *int     `json:"offset,omitempty"`
	EvidenceID *int64   `json:"evidenceId,omitempty"`
	Round      *int     `json:"round,omitempty"`
	Fields     []string `json:"fields,omitempty"`
}

func decodeNativeContextParams(params map[string]any) (nativeContextParams, error) {
	var out nativeContextParams
	raw, err := json.Marshal(params)
	if err != nil {
		return out, errors.New("invalid query parameters")
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, errors.New("query limit, offset and evidenceId must be integers")
	}
	return out, nil
}

func (r *nativeRunner) queryTaskIndex(ctx context.Context, projectID, taskID string, limit, offset int) ([]map[string]any, error) {
	query := `SELECT id,value FROM runner_tasks WHERE project_id=? AND json_extract(value,'$.kind') <> 'planner'`
	args := []any{projectID}
	if taskID != "" {
		query += ` AND id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY CASE json_extract(value,'$.state') WHEN 'active' THEN 0 WHEN 'prepared' THEN 0 WHEN 'returned' THEN 0 WHEN 'canceling' THEN 0 WHEN 'queued' THEN 1 WHEN 'pending_plan' THEN 1 WHEN 'deferred' THEN 1 WHEN 'accepted' THEN 2 ELSE 3 END, rowid DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.scanTaskIndex(ctx, query, args...)
}

func (r *nativeRunner) queryTask(ctx context.Context, projectID, taskID string) (map[string]any, error) {
	if taskID == "" {
		return nil, errors.New("taskId is required")
	}
	var raw string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM runner_tasks WHERE project_id=? AND id=?`, projectID, taskID).Scan(&raw); err != nil {
		return nil, err
	}
	var task nativeRunnerTask
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		return nil, err
	}
	return nativePacketUnfinishedTask(task), nil
}

func selectTaskFields(item map[string]any, fields []string) (map[string]any, error) {
	allowed := map[string]bool{"waitFor": true, "waitReview": true, "objective": true, "acceptance": true, "after": true, "parent": true, "priority": true, "observation": true, "correction": true, "recovery": true, "id": true, "projectId": true, "kind": true, "title": true, "state": true, "round": true, "scope": true, "context": true, "checks": true, "validations": true, "goalVersion": true, "result": true, "lastError": true}
	out := map[string]any{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if !allowed[field] {
			return nil, fmt.Errorf("unsupported task field %q", field)
		}
		if value, ok := item[field]; ok {
			out[field] = value
		}
	}
	return out, nil
}

func (r *nativeRunner) lazyIndexTaskEvidence(ctx context.Context, projectID, taskID string, round *int) error {
	var raw string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM runner_tasks WHERE project_id=? AND id=?`, projectID, taskID).Scan(&raw); err != nil {
		return err
	}
	var task nativeRunnerTask
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		return err
	}
	results := []struct {
		round  int
		result *nativeRunnerResult
	}{{task.Round, task.Result}}
	if round != nil {
		results = nil
		for _, attempt := range task.History {
			if attempt.Round == *round {
				results = append(results, struct {
					round  int
					result *nativeRunnerResult
				}{attempt.Round, attempt.Result})
			}
		}
	}
	for _, entry := range results {
		if entry.result == nil || strings.TrimSpace(entry.result.Path) == "" || strings.TrimSpace(entry.result.EventID) == "" {
			continue
		}
		var exists int
		err := r.db.QueryRowContext(ctx, `SELECT 1 FROM runner_evidence WHERE project_id=? AND task_id=? AND round=? AND event_id=?`, projectID, taskID, entry.round, entry.result.EventID).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		data, err := os.ReadFile(entry.result.Path)
		if err != nil {
			return fmt.Errorf("read recorded evidence %s: %w", entry.result.Path, err)
		}
		copy := task
		copy.Round = entry.round
		if err := r.indexEvidence(ctx, copy, *entry.result, data); err != nil {
			return err
		}
	}
	return nil
}

func (r *nativeRunner) scanTaskIndex(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var t nativeRunnerTask
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		out = append(out, compactTask(t))
	}
	return out, rows.Err()
}

func compactTask(t nativeRunnerTask) map[string]any {
	return map[string]any{"id": t.ID, "projectId": t.ProjectID, "kind": t.Kind, "title": t.Title, "state": t.State, "round": t.Round, "lastError": boundedContextText(t.LastError, 512), "result": compactResult(t.Result)}
}

func compactResult(result *nativeRunnerResult) any {
	if result == nil {
		return nil
	}
	return map[string]any{"eventId": result.EventID, "outcome": result.Outcome, "summary": boundedContextText(result.Summary, 512), "sha256": result.SHA256, "terminal": result.Terminal, "errorCode": result.ErrorCode}
}

func boundedContextText(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func (r *nativeRunner) queryHistory(ctx context.Context, projectID, taskID string, limit, offset int) ([]map[string]any, error) {
	if taskID == "" {
		return nil, errors.New("taskId is required")
	}
	items, err := r.scanTaskIndex(ctx, `SELECT id,value FROM runner_tasks WHERE project_id=? AND id=?`, projectID, taskID)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	var raw string
	if err = r.db.QueryRowContext(ctx, `SELECT value FROM runner_tasks WHERE project_id=? AND id=?`, projectID, taskID).Scan(&raw); err != nil {
		return nil, err
	}
	var t nativeRunnerTask
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	start := offset
	end := len(t.History)
	if start > end {
		start = end
	}
	if end > start+limit {
		end = start + limit
	}
	for _, h := range t.History[start:end] {
		out = append(out, map[string]any{"round": h.Round, "goalVersion": h.GoalVersion, "result": compactResult(h.Result), "correction": boundedContextText(h.Correction, 512), "acked": h.Acked, "superseded": h.Superseded, "inactiveProof": h.InactiveProof})
	}
	return out, nil
}

func (r *nativeRunner) queryEvidenceIndex(ctx context.Context, projectID, taskID string, limit, offset int) ([]map[string]any, error) {
	query := `SELECT id,task_id,round,event_id,kind,status,summary,source_path,sha256,bytes,created FROM runner_evidence WHERE project_id=?`
	args := []any{projectID}
	if taskID != "" {
		query += ` AND task_id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, round, bytes, created int64
		var tid, eid, kind, status, summary, path, digest string
		if err := rows.Scan(&id, &tid, &round, &eid, &kind, &status, &summary, &path, &digest, &bytes, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"evidenceId": id, "taskId": tid, "round": round, "eventId": eid, "kind": kind, "status": status, "summary": boundedContextText(summary, 512), "sourcePath": path, "sha256": digest, "bytes": bytes, "created": created})
	}
	return out, rows.Err()
}

func (r *nativeRunner) queryEvidenceContent(ctx context.Context, projectID, taskID string, id int64, offset, limit int) (map[string]any, error) {
	if limit > nativeEvidenceTextMax {
		limit = nativeEvidenceTextMax
	}
	if limit <= 0 {
		limit = nativeEvidenceTextDefault
	}
	var tid, eventID, kind, status, summary, path, digest string
	var round, bytes, created int64
	var content string
	err := r.db.QueryRowContext(ctx, `SELECT task_id,round,event_id,kind,status,summary,source_path,sha256,bytes,substr(CAST(content AS TEXT),?+1,?),created FROM runner_evidence WHERE id=? AND project_id=?`, offset, limit, id, projectID).Scan(&tid, &round, &eventID, &kind, &status, &summary, &path, &digest, &bytes, &content, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("evidence does not belong to project")
	}
	if err != nil {
		return nil, err
	}
	if taskID != "" && taskID != tid {
		return nil, errors.New("evidence does not belong to task")
	}
	piece := content
	if !utf8.ValidString(piece) {
		return nil, errors.New("evidence content is not valid UTF-8")
	}
	return map[string]any{"evidenceId": id, "taskId": tid, "round": round, "eventId": eventID, "kind": kind, "status": status, "summary": summary, "sourcePath": path, "sha256": digest, "bytes": bytes, "created": created, "content": piece, "contentOffset": offset, "contentNextOffset": offset + len([]rune(piece)), "contentHasMore": len([]rune(piece)) == limit}, nil
}
