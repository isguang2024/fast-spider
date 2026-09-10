package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// persistedJobRecord is deliberately limited to data needed to reconstruct a
// stable Watch result. A process handle is never persisted or resurrected.
type persistedJobRecord struct {
	ID              string     `json:"id"`
	RequestID       string     `json:"requestId,omitempty"`
	TraceID         string     `json:"traceId,omitempty"`
	Runtime         string     `json:"runtime"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	SpecHash        string     `json:"specHash"`
	State           string     `json:"state"`
	ExitCode        *int       `json:"exitCode,omitempty"`
	Error           string     `json:"error,omitempty"`
	ReceivedAt      time.Time  `json:"receivedAt"`
	StartedAt       time.Time  `json:"startedAt"`
	FinishedAt      time.Time  `json:"finishedAt,omitempty"`
	Events          []JobEvent `json:"events,omitempty"`
	NextSequence    int64      `json:"nextSequence"`
	TruncatedBefore int64      `json:"truncatedBefore,omitempty"`
	LogPath         string     `json:"logPath,omitempty"`
	PID             int        `json:"pid,omitempty"`
	ProcessPath     string     `json:"processPath,omitempty"`
	ProcessBirth    string     `json:"processBirth,omitempty"`
}

type persistedProcessIdentity struct {
	PID   int
	Path  string
	Birth string
}

type persistedProcessState uint8

const (
	persistedProcessUnknown persistedProcessState = iota
	persistedProcessStopped
	persistedProcessRunning
)

type jobStore struct {
	path string
	mu   sync.Mutex
}

var jobStoreFileMu sync.Mutex

func newJobStore(dataDir string) (*jobStore, error) {
	if dataDir == "" {
		return nil, nil
	}
	dir := filepath.Join(dataDir, "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &jobStore{path: filepath.Join(dir, "records.json")}, nil
}

func (s *jobStore) load() ([]persistedJobRecord, error) {
	if s == nil || s.path == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobStoreFileMu.Lock()
	defer jobStoreFileMu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []persistedJobRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *jobStore) replace(records []persistedJobRecord) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobStoreFileMu.Lock()
	defer jobStoreFileMu.Unlock()
	raw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".records-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceJobStoreFile(tmpPath, s.path); err != nil {
		return err
	}
	return nil
}

func (s *jobStore) close() error {
	return nil
}

func persistedJobRecordFrom(job *Job, specHash string) persistedJobRecord {
	job.mu.Lock()
	defer job.mu.Unlock()
	identity := persistedProcessIdentityForCommand(job.cmd)
	return persistedJobRecord{
		ID: job.id, RequestID: job.requestID, TraceID: job.traceID, Runtime: job.runtime,
		IdempotencyKey: job.idempotencyKey, SpecHash: specHash, State: job.state,
		ExitCode: job.exitCode, Error: job.errText, ReceivedAt: job.receivedAt,
		StartedAt: job.startedAt, FinishedAt: job.finishedAt, Events: append([]JobEvent(nil), job.events...),
		NextSequence: job.nextSequence, TruncatedBefore: job.truncatedBefore, LogPath: job.logPath,
		PID:          identity.PID,
		ProcessPath:  identity.Path,
		ProcessBirth: identity.Birth,
	}
}

func jobFromPersistedRecord(record persistedJobRecord) *Job {
	job := &Job{
		id: record.ID, requestID: record.RequestID, traceID: record.TraceID, runtime: record.Runtime,
		idempotencyKey: record.IdempotencyKey, state: record.State, exitCode: record.ExitCode,
		errText: record.Error, receivedAt: record.ReceivedAt, startedAt: record.StartedAt,
		finishedAt: record.FinishedAt, events: append([]JobEvent(nil), record.Events...),
		nextSequence: record.NextSequence, truncatedBefore: record.TruncatedBefore, logPath: record.LogPath,
		notify: make(chan struct{}), done: make(chan struct{}), stop: make(chan string, 1),
	}
	job.recovery = persistedProcessIdentity{PID: record.PID, Path: record.ProcessPath, Birth: record.ProcessBirth}
	for _, event := range job.events {
		job.eventBytes += len(event.Text)
	}
	if job.nextSequence == 0 && len(job.events) > 0 {
		job.nextSequence = job.events[len(job.events)-1].Sequence
	}
	if isTerminalJobState(job.state) {
		close(job.done)
	}
	return job
}

func recoverPersistedProcess(record persistedJobRecord) (bool, error) {
	identity := persistedProcessIdentity{PID: record.PID, Path: record.ProcessPath, Birth: record.ProcessBirth}
	return recoverPersistedIdentity(identity)
}

func recoverPersistedIdentity(identity persistedProcessIdentity) (bool, error) {
	state, stateErr := persistedProcessStateOf(identity)
	if state == persistedProcessStopped {
		return true, nil
	}
	if state == persistedProcessUnknown {
		if stateErr == nil {
			stateErr = errors.New("persisted process identity could not be verified")
		}
		return false, stateErr
	}
	if err := stopPersistedProcess(identity); err != nil {
		return false, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr = persistedProcessStateOf(identity)
		if state == persistedProcessStopped {
			return true, nil
		}
		if state == persistedProcessUnknown && stateErr != nil {
			return false, stateErr
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, errors.New("persisted process did not stop")
}

func (m *JobManager) recoverUnknownJob(job *Job) error {
	if m == nil || job == nil {
		return nil
	}
	job.mu.Lock()
	if job.state != "unknown" {
		job.mu.Unlock()
		return nil
	}
	identity := job.recovery
	job.mu.Unlock()
	stopped, recoverErr := recoverPersistedIdentity(identity)
	if !stopped {
		_ = recoverErr
		return nil
	}
	job.mu.Lock()
	if job.state == "unknown" {
		job.state = "interrupted"
		job.errText = "job process was confirmed stopped after Node restart"
		job.finishedAt = time.Now().UTC()
		job.appendEventLocked("interrupted", job.errText, job.finishedAt)
		close(job.done)
	}
	job.mu.Unlock()
	return m.persistJob(job)
}

func (m *JobManager) restorePersistedJobs() error {
	if m == nil || m.store == nil {
		return nil
	}
	records, err := m.store.load()
	if err != nil {
		return err
	}
	changed := false
	for _, record := range records {
		if record.ID == "" || record.IdempotencyKey == "" {
			continue
		}
		if record.State != "starting" && record.State != "running" && !isTerminalJobState(record.State) && record.State != "unknown" {
			record.State = "unknown"
			record.Error = "job state was not recoverable after Node restart"
			record.NextSequence++
			record.Events = append(record.Events, JobEvent{Sequence: record.NextSequence, Type: "unknown", Text: record.Error, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
			changed = true
		}
		if record.State == "starting" || record.State == "running" {
			stopped, recoverErr := recoverPersistedProcess(record)
			if stopped {
				record.State = "interrupted"
				record.Error = "job interrupted by Node restart; process was stopped and not resumed"
				record.FinishedAt = time.Now().UTC()
			} else {
				record.State = "unknown"
				record.Error = "job process state is unknown after Node restart"
				if recoverErr != nil {
					record.Error += ": " + recoverErr.Error()
				}
			}
			record.NextSequence++
			eventType := "interrupted"
			if record.State == "unknown" {
				eventType = "unknown"
			}
			record.Events = append(record.Events, JobEvent{Sequence: record.NextSequence, Type: eventType, Text: record.Error, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
			changed = true
		}
		job := jobFromPersistedRecord(record)
		m.jobs[job.id] = job
		m.order = append(m.order, job.id)
		m.idempotency[record.IdempotencyKey] = idempotencyRecord{JobID: record.ID, SpecHash: record.SpecHash}
	}
	if changed {
		return m.persistLocked()
	}
	return nil
}

func (m *JobManager) persistedRecordsLocked(extra *persistedJobRecord) []persistedJobRecord {
	records := make([]persistedJobRecord, 0, len(m.order)+1)
	for _, jobID := range m.order {
		job := m.jobs[jobID]
		if job == nil {
			continue
		}
		key := job.idempotencyKey
		records = append(records, persistedJobRecordFrom(job, m.idempotency[key].SpecHash))
	}
	if extra != nil {
		records = append(records, *extra)
	}
	return records
}

func (m *JobManager) persistLocked() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.replace(m.persistedRecordsLocked(nil))
}

func (m *JobManager) persistStartingLocked(jobID, requestID, traceID, runtime, key, specHash string, receivedAt time.Time) error {
	if m == nil || m.store == nil {
		return nil
	}
	record := persistedJobRecord{ID: jobID, RequestID: requestID, TraceID: traceID, Runtime: runtime, IdempotencyKey: key, SpecHash: specHash, State: "starting", ReceivedAt: receivedAt, StartedAt: time.Now().UTC()}
	return m.store.replace(m.persistedRecordsLocked(&record))
}

func (m *JobManager) persistJob(job *Job) error {
	if m == nil || m.store == nil || job == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var records []persistedJobRecord
	for _, jobID := range m.order {
		current := m.jobs[jobID]
		if current == nil {
			continue
		}
		currentKey := current.idempotencyKey
		hash := m.idempotency[currentKey].SpecHash
		records = append(records, persistedJobRecordFrom(current, hash))
	}
	return m.store.replace(records)
}

// NativeRunnerJobSpec is the stable Node-facing execution contract used by
// the agent adapter. It keeps executionRuntime private while allowing the
// native runner to reuse JobManager's process and timeout implementation.
type NativeRunnerJobSpec struct {
	Cwd            string
	Argv           []string
	Runtime        string
	Timeout        time.Duration
	IdempotencyKey string
}

type NativeRunnerJobSnapshot struct {
	JobID    string
	Runtime  string
	State    string
	ExitCode *int
	Error    string
	Evidence string
}

type NativeRunnerJobExecutor struct{ manager *JobManager }

func NewNativeRunnerJobExecutor(manager *JobManager) *NativeRunnerJobExecutor {
	if manager == nil {
		return nil
	}
	return &NativeRunnerJobExecutor{manager: manager}
}

func (e *NativeRunnerJobExecutor) Start(ctx context.Context, spec NativeRunnerJobSpec) (NativeRunnerJobSnapshot, error) {
	if e == nil || e.manager == nil {
		return NativeRunnerJobSnapshot{}, errors.New("native runner job manager is unavailable")
	}
	runtimeSpec := executionRuntime{Kind: spec.Runtime}
	if runtimeSpec.Kind == "" {
		runtimeSpec.Kind = "host"
	}
	snapshot, err := e.manager.StartExecution(ctx, spec.Cwd, spec.Argv, runtimeSpec, spec.Timeout, spec.IdempotencyKey, "", "")
	if err != nil {
		return NativeRunnerJobSnapshot{}, err
	}
	return nativeRunnerJobSnapshotFromJob(snapshot), nil
}

func (e *NativeRunnerJobExecutor) Watch(ctx context.Context, jobID string) (NativeRunnerJobSnapshot, error) {
	if e == nil || e.manager == nil {
		return NativeRunnerJobSnapshot{}, errors.New("native runner job manager is unavailable")
	}
	snapshot, err := e.manager.Watch(ctx, jobID, 0, 0)
	if err != nil {
		return NativeRunnerJobSnapshot{}, err
	}
	result := nativeRunnerJobSnapshotFromJob(snapshot)
	if isTerminalJobState(snapshot.State) && snapshot.State != "interrupted" {
		if path, size, truncated, logErr := e.manager.JobLog(jobID); logErr == nil {
			result.Evidence = fmt.Sprintf("job log: %s (%d bytes)", path, size)
			if truncated {
				result.Evidence = fmt.Sprintf("%s, truncated", result.Evidence)
			}
		}
	}
	return result, nil
}

func nativeRunnerJobSnapshotFromJob(snapshot JobSnapshot) NativeRunnerJobSnapshot {
	return NativeRunnerJobSnapshot{JobID: snapshot.JobID, Runtime: snapshot.Runtime, State: snapshot.State, ExitCode: snapshot.ExitCode, Error: snapshot.Error, Evidence: snapshot.Error}
}
