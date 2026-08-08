package node

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxConcurrentJobs   = 16
	maxRetainedJobs     = 128
	maxJobEvents        = 1024
	maxJobEventBytes    = 96 << 10
	maxJobArgCount      = 64
	maxJobArgBytes      = 4096
	maxJobArgTotalBytes = 64 << 10
	maxJobTimeout       = 30 * time.Minute
	defaultJobTimeout   = 10 * time.Minute
	maxJobWatchWait     = 15 * time.Second
	maxScannerTokenSize = 64 << 10
)

var (
	ErrJobNotFound         = errors.New("job not found")
	ErrJobLimit            = errors.New("job limit reached")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with an existing job")
)

type JobEvent struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Timestamp string `json:"timestamp"`
}

type JobSnapshot struct {
	JobID           string     `json:"jobId"`
	State           string     `json:"state"`
	ExitCode        *int       `json:"exitCode,omitempty"`
	Error           string     `json:"error,omitempty"`
	Events          []JobEvent `json:"events"`
	NextCursor      int64      `json:"nextCursor"`
	TruncatedBefore int64      `json:"truncatedBefore,omitempty"`
	StartedAt       string     `json:"startedAt"`
	FinishedAt      string     `json:"finishedAt,omitempty"`
}

type idempotencyRecord struct {
	JobID    string
	SpecHash string
}

type Job struct {
	mu              sync.Mutex
	id              string
	idempotencyKey  string
	state           string
	exitCode        *int
	errText         string
	startedAt       time.Time
	finishedAt      time.Time
	events          []JobEvent
	eventBytes      int
	nextSequence    int64
	truncatedBefore int64
	notify          chan struct{}
	cmd             *exec.Cmd
	stop            chan string
	done            chan struct{}
	stopReason      string
}

type JobManager struct {
	mu          sync.RWMutex
	jobs        map[string]*Job
	order       []string
	idempotency map[string]idempotencyRecord
	semaphore   chan struct{}
}

func NewJobManager() *JobManager {
	return &JobManager{
		jobs:        make(map[string]*Job),
		idempotency: make(map[string]idempotencyRecord),
		semaphore:   make(chan struct{}, maxConcurrentJobs),
	}
}

func (m *JobManager) StartShell(workspaceID, cwd string, argv []string, timeout time.Duration, idempotencyKey string, permissionGuard func() bool) (JobSnapshot, error) {
	if err := validateShellSpec(argv, timeout, idempotencyKey); err != nil {
		return JobSnapshot{}, err
	}
	if timeout == 0 {
		timeout = defaultJobTimeout
	}
	specHash := shellSpecHash(workspaceID, cwd, argv, timeout)

	m.mu.Lock()
	if previous, ok := m.idempotency[idempotencyKey]; ok {
		job := m.jobs[previous.JobID]
		m.mu.Unlock()
		if previous.SpecHash != specHash {
			return JobSnapshot{}, ErrIdempotencyConflict
		}
		if job == nil {
			return JobSnapshot{}, ErrJobNotFound
		}
		snapshot, _ := job.snapshotAfter(0)
		snapshot.Events = []JobEvent{}
		return snapshot, nil
	}
	m.cleanupLocked()
	if len(m.jobs) >= maxRetainedJobs {
		m.mu.Unlock()
		return JobSnapshot{}, ErrJobLimit
	}
	select {
	case m.semaphore <- struct{}{}:
	default:
		m.mu.Unlock()
		return JobSnapshot{}, ErrJobLimit
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = safeShellEnvironment()
	configureProcessTree(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	if err := cmd.Start(); err != nil {
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	jobID, err := security.RandomOpaque("job_")
	if err != nil {
		_ = killProcessTree(cmd)
		_ = cmd.Wait()
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	now := time.Now().UTC()
	job := &Job{
		id: jobID, idempotencyKey: idempotencyKey, state: "running", startedAt: now,
		notify: make(chan struct{}), cmd: cmd, stop: make(chan string, 1), done: make(chan struct{}),
	}
	job.appendEvent("started", "process started")

	m.jobs[jobID] = job
	m.order = append(m.order, jobID)
	m.idempotency[idempotencyKey] = idempotencyRecord{JobID: jobID, SpecHash: specHash}
	m.mu.Unlock()

	go m.runJob(job, stdout, stderr, timeout, permissionGuard)
	snapshot, _ := job.snapshotAfter(0)
	return snapshot, nil
}

func (m *JobManager) runJob(job *Job, stdout, stderr interface{ Read([]byte) (int, error) }, timeout time.Duration, permissionGuard func() bool) {
	var streams sync.WaitGroup
	streams.Add(2)
	go func() {
		defer streams.Done()
		job.captureStream("stdout", stdout)
	}()
	go func() {
		defer streams.Done()
		job.captureStream("stderr", stderr)
	}()

	timer := time.NewTimer(timeout)
	permissionTicker := time.NewTicker(2 * time.Second)
	defer permissionTicker.Stop()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			select {
			case reason := <-job.stop:
				job.setStopReason(reason)
				_ = killProcessTree(job.cmd)
				return
			case <-timer.C:
				job.setStopReason("timeout")
				_ = killProcessTree(job.cmd)
				return
			case <-permissionTicker.C:
				if permissionGuard != nil && !permissionGuard() {
					job.setStopReason("permission_revoked")
					_ = killProcessTree(job.cmd)
					return
				}
			case <-job.done:
				return
			}
		}
	}()

	waitErr := job.cmd.Wait()
	streams.Wait()
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	close(job.done)
	<-watchDone
	job.finish(waitErr)
	<-m.semaphore
}

func (m *JobManager) Watch(ctx context.Context, jobID string, cursor int64, wait time.Duration) (JobSnapshot, error) {
	if cursor < 0 {
		return JobSnapshot{}, fmt.Errorf("cursor must be non-negative")
	}
	if wait < 0 || wait > maxJobWatchWait {
		return JobSnapshot{}, fmt.Errorf("wait must be between 0 and %s", maxJobWatchWait)
	}
	m.mu.RLock()
	job := m.jobs[jobID]
	m.mu.RUnlock()
	if job == nil {
		return JobSnapshot{}, ErrJobNotFound
	}
	deadline := time.NewTimer(wait)
	if wait == 0 {
		if !deadline.Stop() {
			<-deadline.C
		}
	}
	defer deadline.Stop()
	for {
		snapshot, notify := job.snapshotAfter(cursor)
		if len(snapshot.Events) > 0 || isTerminalJobState(snapshot.State) || wait == 0 {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return JobSnapshot{}, ctx.Err()
		case <-deadline.C:
			return snapshot, nil
		case <-notify:
		}
	}
}

func (m *JobManager) Cancel(ctx context.Context, jobID string) (JobSnapshot, error) {
	m.mu.RLock()
	job := m.jobs[jobID]
	m.mu.RUnlock()
	if job == nil {
		return JobSnapshot{}, ErrJobNotFound
	}
	job.mu.Lock()
	terminal := isTerminalJobState(job.state)
	job.mu.Unlock()
	if !terminal {
		select {
		case job.stop <- "user":
		default:
		}
	}
	for {
		snapshot, notify := job.snapshotAfter(0)
		if isTerminalJobState(snapshot.State) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return JobSnapshot{}, ctx.Err()
		case <-notify:
		}
	}
}

func (m *JobManager) CancelAll(ctx context.Context) error {
	m.mu.RLock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.RUnlock()
	for _, job := range jobs {
		job.mu.Lock()
		terminal := isTerminalJobState(job.state)
		job.mu.Unlock()
		if !terminal {
			select {
			case job.stop <- "shutdown":
			default:
			}
		}
	}
	for _, job := range jobs {
		for {
			snapshot, notify := job.snapshotAfter(0)
			if isTerminalJobState(snapshot.State) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-notify:
			}
		}
	}
	return nil
}

func (m *JobManager) cleanupLocked() {
	if len(m.jobs) < maxRetainedJobs {
		return
	}
	kept := m.order[:0]
	for _, jobID := range m.order {
		job := m.jobs[jobID]
		if job == nil {
			continue
		}
		job.mu.Lock()
		terminal := isTerminalJobState(job.state)
		key := job.idempotencyKey
		job.mu.Unlock()
		if terminal && len(m.jobs) >= maxRetainedJobs {
			delete(m.jobs, jobID)
			delete(m.idempotency, key)
			continue
		}
		kept = append(kept, jobID)
	}
	m.order = kept
}

func (j *Job) captureStream(eventType string, reader interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenSize)
	for scanner.Scan() {
		text := scanner.Text()
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "�")
			j.appendEvent("warning", eventType+" contained invalid UTF-8 and was normalized")
		}
		j.appendEvent(eventType, text+"\n")
	}
	if err := scanner.Err(); err != nil {
		j.appendEvent("warning", eventType+" capture stopped: "+err.Error())
	}
}

func (j *Job) appendEvent(eventType, text string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.nextSequence++
	event := JobEvent{Sequence: j.nextSequence, Type: eventType, Text: text, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	j.events = append(j.events, event)
	j.eventBytes += len(text)
	for len(j.events) > maxJobEvents || j.eventBytes > maxJobEventBytes {
		dropped := j.events[0]
		j.events = j.events[1:]
		j.eventBytes -= len(dropped.Text)
		j.truncatedBefore = dropped.Sequence
	}
	close(j.notify)
	j.notify = make(chan struct{})
}

func (j *Job) setStopReason(reason string) {
	j.mu.Lock()
	if j.stopReason == "" {
		j.stopReason = reason
	}
	j.mu.Unlock()
}

func (j *Job) finish(waitErr error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cmd.ProcessState != nil {
		code := j.cmd.ProcessState.ExitCode()
		j.exitCode = &code
	}
	j.finishedAt = time.Now().UTC()
	switch j.stopReason {
	case "user", "shutdown":
		if waitErr == nil {
			j.state = "completed"
		} else {
			j.state = "canceled"
		}
	case "permission_revoked":
		if waitErr == nil {
			j.state = "completed"
		} else {
			j.state = "canceled"
			j.errText = "workspace permission revoked"
		}
	case "timeout":
		if waitErr == nil {
			j.state = "completed"
		} else {
			j.state = "expired"
		}
	default:
		if waitErr == nil {
			j.state = "completed"
		} else {
			j.state = "failed"
			j.errText = waitErr.Error()
		}
	}
	j.nextSequence++
	j.events = append(j.events, JobEvent{Sequence: j.nextSequence, Type: j.state, Text: j.errText, Timestamp: j.finishedAt.Format(time.RFC3339Nano)})
	j.eventBytes += len(j.errText)
	for len(j.events) > maxJobEvents || j.eventBytes > maxJobEventBytes {
		dropped := j.events[0]
		j.events = j.events[1:]
		j.eventBytes -= len(dropped.Text)
		j.truncatedBefore = dropped.Sequence
	}
	close(j.notify)
	j.notify = make(chan struct{})
}

func (j *Job) snapshotAfter(cursor int64) (JobSnapshot, <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := JobSnapshot{
		JobID: j.id, State: j.state, ExitCode: j.exitCode, Error: j.errText,
		NextCursor: j.nextSequence, TruncatedBefore: j.truncatedBefore,
		StartedAt: j.startedAt.Format(time.RFC3339Nano), Events: []JobEvent{},
	}
	if !j.finishedAt.IsZero() {
		out.FinishedAt = j.finishedAt.Format(time.RFC3339Nano)
	}
	for _, event := range j.events {
		if event.Sequence > cursor {
			out.Events = append(out.Events, event)
		}
	}
	return out, j.notify
}

func validateShellSpec(argv []string, timeout time.Duration, idempotencyKey string) error {
	if len(argv) == 0 || len(argv) > maxJobArgCount {
		return fmt.Errorf("argv must contain 1 to %d items", maxJobArgCount)
	}
	totalBytes := 0
	for _, arg := range argv {
		if len(arg) == 0 || len(arg) > maxJobArgBytes || strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("argv contains an invalid argument")
		}
		totalBytes += len(arg)
		if totalBytes > maxJobArgTotalBytes {
			return fmt.Errorf("argv exceeds total size limit")
		}
	}
	if timeout < 0 || timeout > maxJobTimeout {
		return fmt.Errorf("timeout exceeds limit")
	}
	if len(idempotencyKey) < 12 || len(idempotencyKey) > 128 {
		return fmt.Errorf("idempotencyKey must be 12 to 128 characters")
	}
	return nil
}

func shellSpecHash(workspaceID, cwd string, argv []string, timeout time.Duration) string {
	raw, _ := json.Marshal(map[string]any{"workspaceId": workspaceID, "cwd": cwd, "argv": argv, "timeout": timeout.String()})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isTerminalJobState(state string) bool {
	switch state {
	case "completed", "failed", "canceled", "expired":
		return true
	default:
		return false
	}
}
