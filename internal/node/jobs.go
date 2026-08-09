package node

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	maxJobLogBytes      = 10 << 20
	jobRetention        = 24 * time.Hour
)

var (
	ErrJobNotFound         = errors.New("job not found")
	ErrJobLimit            = errors.New("job limit reached")
	ErrJobNotComplete      = errors.New("job is not complete")
	ErrJobLogUnavailable   = errors.New("job log is unavailable")
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
	logPath         string
	logFile         *os.File
	logBytes        int64
	logTruncated    bool
	logErr          string
}

type JobManager struct {
	mu          sync.RWMutex
	jobs        map[string]*Job
	order       []string
	idempotency map[string]idempotencyRecord
	semaphore   chan struct{}
	logDir      string
}

func NewJobManager(dataDirs ...string) *JobManager {
	manager := &JobManager{
		jobs:        make(map[string]*Job),
		idempotency: make(map[string]idempotencyRecord),
		semaphore:   make(chan struct{}, maxConcurrentJobs),
	}
	if len(dataDirs) > 0 && strings.TrimSpace(dataDirs[0]) != "" {
		manager.logDir = filepath.Join(dataDirs[0], "jobs")
		_ = os.MkdirAll(manager.logDir, 0o700)
		manager.cleanupOldJobLogs(time.Now().UTC())
	}
	return manager
}

func (m *JobManager) StartShell(cwd string, argv []string, timeout time.Duration, idempotencyKey string) (JobSnapshot, error) {
	if err := validateShellSpec(argv, timeout, idempotencyKey); err != nil {
		return JobSnapshot{}, err
	}
	if timeout == 0 {
		timeout = defaultJobTimeout
	}
	specHash := shellSpecHash(cwd, argv, timeout)

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

	jobID, err := security.RandomOpaque("job_")
	if err != nil {
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	var logFile *os.File
	var logPath string
	if m.logDir != "" {
		logPath = filepath.Join(m.logDir, jobID+".log")
		logFile, _ = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
		if logFile != nil {
			_ = logFile.Close()
			_ = os.Remove(logPath)
		}
		<-m.semaphore
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	now := time.Now().UTC()
	job := &Job{
		id: jobID, idempotencyKey: idempotencyKey, state: "running", startedAt: now,
		notify: make(chan struct{}), cmd: cmd, stop: make(chan string, 1), done: make(chan struct{}), logPath: logPath, logFile: logFile,
	}
	job.appendEvent("started", "process started")

	m.jobs[jobID] = job
	m.order = append(m.order, jobID)
	m.idempotency[idempotencyKey] = idempotencyRecord{JobID: jobID, SpecHash: specHash}
	m.mu.Unlock()

	go m.runJob(job, stdout, stderr, timeout)
	snapshot, _ := job.snapshotAfter(0)
	return snapshot, nil
}

func (m *JobManager) runJob(job *Job, stdout, stderr interface{ Read([]byte) (int, error) }, timeout time.Duration) {
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

func (m *JobManager) StartMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			m.cleanupLocked()
			m.mu.Unlock()
			m.cleanupOldJobLogs(time.Now().UTC())
		}
	}
}

func (m *JobManager) JobLog(jobID string) (path string, size int64, truncated bool, err error) {
	m.mu.RLock()
	job := m.jobs[jobID]
	m.mu.RUnlock()
	if job == nil {
		return "", 0, false, ErrJobNotFound
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if !isTerminalJobState(job.state) {
		return "", 0, false, ErrJobNotComplete
	}
	if job.logPath == "" || job.logErr != "" {
		return "", 0, job.logTruncated, ErrJobLogUnavailable
	}
	return job.logPath, job.logBytes, job.logTruncated, nil
}

func (m *JobManager) cleanupOldJobLogs(now time.Time) {
	if m.logDir == "" {
		return
	}
	entries, err := os.ReadDir(m.logDir)
	if err != nil {
		return
	}
	cutoff := now.Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(m.logDir, entry.Name()))
		}
	}
}

func (m *JobManager) cleanupLocked() {
	cutoff := time.Now().UTC().Add(-jobRetention)
	kept := m.order[:0]
	for _, jobID := range m.order {
		job := m.jobs[jobID]
		if job == nil {
			continue
		}
		job.mu.Lock()
		terminal := isTerminalJobState(job.state)
		finishedAt := job.finishedAt
		key := job.idempotencyKey
		job.mu.Unlock()
		expired := terminal && !finishedAt.IsZero() && finishedAt.Before(cutoff)
		if terminal && (expired || len(m.jobs) >= maxRetainedJobs) {
			delete(m.jobs, jobID)
			delete(m.idempotency, key)
			if job.logPath != "" {
				_ = os.Remove(job.logPath)
			}
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
	j.appendEventLocked(eventType, text, time.Now().UTC())
}

func (j *Job) appendEventLocked(eventType, text string, now time.Time) {
	j.nextSequence++
	event := JobEvent{Sequence: j.nextSequence, Type: eventType, Text: text, Timestamp: now.Format(time.RFC3339Nano)}
	j.events = append(j.events, event)
	j.eventBytes += len(text)
	if j.logFile != nil && !j.logTruncated && j.logErr == "" {
		line := []byte(event.Timestamp + "\t" + event.Type + "\t" + event.Text)
		remaining := int64(maxJobLogBytes) - j.logBytes
		if remaining <= 0 {
			j.logTruncated = true
		} else {
			if int64(len(line)) > remaining {
				line = line[:remaining]
				j.logTruncated = true
			}
			n, err := j.logFile.Write(line)
			j.logBytes += int64(n)
			if err != nil {
				j.logErr = err.Error()
			}
		}
	}
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
			j.errText = "operation permission revoked"
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
	j.appendEventLocked(j.state, j.errText, j.finishedAt)
	if j.logFile != nil {
		if err := j.logFile.Sync(); err != nil && j.logErr == "" {
			j.logErr = err.Error()
		}
		if err := j.logFile.Close(); err != nil && j.logErr == "" {
			j.logErr = err.Error()
		}
		j.logFile = nil
	}
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

func shellSpecHash(cwd string, argv []string, timeout time.Duration) string {
	raw, _ := json.Marshal(map[string]any{"cwd": cwd, "argv": argv, "timeout": timeout.String()})
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
