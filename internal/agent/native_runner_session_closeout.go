package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Online retention is independent of business acceptance and review evidence.
// No automatic deletion: the provider conversation remains recoverable.
type nativeSessionCloseout struct {
	SessionID  string `json:"sessionId"`
	Round      int    `json:"round"`
	State      string `json:"state"`
	NextAt     int64  `json:"nextAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	ArchivedAt int64  `json:"archivedAt,omitempty"`
	Failures   int    `json:"failures,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

type nativeSessionArchiveBackend interface {
	ArchiveSession(context.Context, nativeRunnerTask) error
}

func nativePacketCloseout(t nativeRunnerTask) map[string]any {
	counts := map[string]int{}
	recent := []nativeSessionCloseout{}
	for i := len(t.SessionCloseout) - 1; i >= 0; i-- {
		s := t.SessionCloseout[i]
		counts[s.State]++
		if s.State != "archived" && len(recent) < 4 {
			recent = append(recent, s)
		}
	}
	return map[string]any{"counts": counts, "pendingSample": recent, "total": len(t.SessionCloseout)}
}

func (a *nativeRunnerAsyncBackend) ArchiveSession(_ context.Context, task nativeRunnerTask) error {
	b, ok := a.backend.(nativeSessionArchiveBackend)
	if !ok {
		return errors.New("online session archive is unavailable")
	}
	_, err := a.start(nativeRunnerAsyncOperationID{kind: "archive", key: task.Receipt.SessionID}, false, nil, func(ctx context.Context) (any, error) {
		return nil, b.ArchiveSession(ctx, task)
	})
	return err
}

func (t *nativeRunnerTransport) ArchiveSession(ctx context.Context, task nativeRunnerTask) error {
	if t == nil || t.manager == nil || t.manager.chatgptCloud == nil {
		return nodeRunnerUnavailableError()
	}
	if task.Receipt == nil || task.Receipt.InDoubt || task.Result == nil || !task.Result.Terminal || task.Result.RecoveryOnly || !task.ResultAcked {
		return errors.New("online archive requires a terminal result and durable callback acknowledgement")
	}
	// Watch only local registered jobs; never stop a running process to archive.
	for _, id := range nativeCloseoutJobIDs(task) {
		result, err := t.WatchCheck(ctx, id)
		if err != nil {
			return err
		}
		switch result.State {
		case "completed", "failed", "canceled":
		default:
			return fmt.Errorf("online archive waits for job %s", id)
		}
	}
	if err := t.manager.chatgptCloud.Archive(ctx, task.Receipt.SessionID, true); err != nil {
		return err
	}
	t.manager.chatgptCloud.ReleaseCallbackRealtimeForGeneration(task.Receipt.SessionID, task.Receipt.Generation)
	return nil
}

func nativeCloseoutJobIDs(task nativeRunnerTask) []string {
	ids := map[string]bool{}
	for _, v := range task.Validations {
		if v.JobID != "" {
			ids[v.JobID] = true
		}
	}
	if task.Recovery != nil {
		for _, id := range task.Recovery.Checkpoint.WaitingJobs {
			if id != "" {
				ids[id] = true
			}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func nativeArchiveCandidates(t nativeRunnerTask) []nativeRunnerTask {
	var out []nativeRunnerTask
	seen := map[string]bool{}
	add := func(candidate nativeRunnerTask) {
		if candidate.Receipt == nil || candidate.Receipt.InDoubt || candidate.Receipt.SessionID == "" || candidate.Result == nil || !candidate.Result.Terminal || candidate.Result.RecoveryOnly || !candidate.ResultAcked {
			return
		}
		id := candidate.Receipt.SessionID
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, candidate)
	}
	terminal := t.State == "accepted" || t.State == "cancelled"
	reuseID := ""
	if !terminal && t.Receipt == nil && !t.Rotate && len(t.History) > 0 {
		if last := t.History[len(t.History)-1].Receipt; last != nil {
			reuseID = last.SessionID
		}
	}
	if terminal {
		add(t)
	}
	for _, h := range t.History {
		// A session reused by the current generation must remain available.
		if h.Receipt == nil || (t.Receipt != nil && h.Receipt.SessionID == t.Receipt.SessionID && !terminal) {
			continue
		}
		if !terminal && ((t.Request != nil && t.Request.TargetSessionID == h.Receipt.SessionID) || reuseID == h.Receipt.SessionID) {
			continue
		}
		old := t
		old.Round = h.Round
		old.Request = h.Request
		old.Receipt = h.Receipt
		old.Result = h.Result
		old.ResultAcked = h.Acked
		old.Validations = nil
		old.Recovery = &nativeRunnerRecovery{Checkpoint: nativeRunnerCheckpoint{WaitingJobs: h.CloseoutJobs}}
		add(old)
	}
	return out
}

func (r *nativeRunner) tickSessionCloseout(ctx context.Context, tasks []nativeRunnerTask) error {
	b, ok := r.backend.(nativeSessionArchiveBackend)
	if !ok {
		return nil
	}
	// Consume the exact in-flight operation even if a new user plan changed the
	// task meanwhile. Never strand the global housekeeping worker on eligibility.
	if r.closeoutTask != nil {
		pending := *r.closeoutTask
		err := b.ArchiveSession(ctx, pending)
		if errors.Is(err, errNativeRunnerOperationPending) {
			return nil
		}
		r.closeoutTask = nil
		r.closeoutPending = ""
		for i := range tasks {
			if tasks[i].ID == pending.ID {
				for j := range tasks[i].SessionCloseout {
					if tasks[i].SessionCloseout[j].SessionID == pending.Receipt.SessionID {
						r.finishSessionCloseout(&tasks[i].SessionCloseout[j], err)
					}
				}
				if saveErr := r.saveTask(ctx, tasks[i], "session_closeout"); saveErr != nil {
					return saveErr
				}
			}
		}
	}
	// Restore provider archive cooldown after restart, without delaying business.
	for _, task := range tasks {
		for _, s := range task.SessionCloseout {
			if s.HTTPStatus == 429 && s.NextAt > r.closeoutNextAt {
				r.closeoutNextAt = s.NextAt
			}
		}
	}
	for _, task := range tasks {
		changed := false
		for _, candidate := range nativeArchiveCandidates(task) {
			id := candidate.Receipt.SessionID
			idx := -1
			for i := range task.SessionCloseout {
				if task.SessionCloseout[i].SessionID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				task.SessionCloseout = append(task.SessionCloseout, nativeSessionCloseout{SessionID: id, Round: candidate.Round, State: "pending"})
				idx = len(task.SessionCloseout) - 1
				changed = true
			}
			s := &task.SessionCloseout[idx]
			if s.State == "archived" || s.NextAt > r.now().Unix() {
				continue
			}
			key := task.ID + ":" + id
			if r.closeoutPending != "" && r.closeoutPending != key {
				continue
			}
			if r.closeoutPending == "" && (r.closeoutNextAt > r.now().Unix() || r.cooldownUntil > r.now().Unix()) {
				continue
			}
			err := b.ArchiveSession(ctx, candidate)
			if errors.Is(err, errNativeRunnerOperationPending) {
				r.closeoutPending = key
				copy := candidate
				r.closeoutTask = &copy
				if s.State != "archiving" {
					s.State = "archiving"
					changed = true
				}
				continue
			}
			changed = true
			r.finishSessionCloseout(s, err)
		}
		if changed {
			if err := r.saveTask(ctx, task, "session_closeout"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *nativeRunner) finishSessionCloseout(s *nativeSessionCloseout, err error) {
	r.closeoutPending = ""
	r.closeoutNextAt = r.now().Add(30 * time.Second).Unix()
	if err == nil {
		s.State = "archived"
		s.ArchivedAt = r.now().Unix()
		s.LastError = ""
		s.NextAt = 0
		s.HTTPStatus = 0
		return
	}
	s.State = "retry"
	s.Failures++
	s.LastError = err.Error()
	s.HTTPStatus = 0
	delay := time.Minute << min(s.Failures-1, 6)
	var limited *chatGPTCloudHTTPError
	if errors.As(err, &limited) {
		s.HTTPStatus = limited.status
		if after := chatGPTCloudRetryAfterDuration(limited.retryAfter, r.now()); after > delay {
			delay = after
		}
		if limited.status == 429 {
			r.closeoutNextAt = r.now().Add(delay).Unix()
		}
	}
	s.NextAt = r.now().Add(delay).Unix()
}
