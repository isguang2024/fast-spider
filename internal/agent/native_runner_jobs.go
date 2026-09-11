package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/hostapi"
)

const (
	maxNativeRunnerJobTimeout  = 30 * time.Minute
	maxNativeRunnerJobKeySize  = 128
	minNativeRunnerJobKeySize  = 12
	maxNativeRunnerJobArgCount = 64
	maxNativeRunnerJobArgBytes = 4096
	maxNativeRunnerJobArgTotal = 64 << 10
)

// The Node JobManager is the only durable authority for check jobs. The public
// hostapi contract keeps the specialized runner independent of internal/node.
type nativeRunnerJobSpec = hostapi.NativeRunnerJobSpec
type nativeRunnerJobSnapshot = hostapi.NativeRunnerJobSnapshot
type nativeRunnerJobExecutor = hostapi.NativeRunnerJobExecutor

type nativeRunnerJobs struct {
	executor nativeRunnerJobExecutor
}

func newNativeRunnerJobs(_ string, executor nativeRunnerJobExecutor) (*nativeRunnerJobs, error) {
	if executor == nil {
		return nil, errors.New("native runner job executor is nil")
	}
	return &nativeRunnerJobs{executor: executor}, nil
}

func (j *nativeRunnerJobs) Close() error { return nil }

func (j *nativeRunnerJobs) StartCheck(ctx context.Context, request nativeRunnerCheckRequest) (string, error) {
	spec, err := nativeRunnerJobSpecFromCheck(request)
	if err != nil {
		return "", err
	}
	if j == nil || j.executor == nil {
		return "", errors.New("native runner job executor is unavailable")
	}
	snapshot, err := j.executor.Start(ctx, spec)
	if err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(snapshot.JobID)
	if jobID == "" {
		return "", errors.New("native runner job executor returned an empty job id")
	}
	return jobID, nil
}

func (j *nativeRunnerJobs) WatchCheck(ctx context.Context, jobID string) (nativeRunnerCheckResult, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nativeRunnerCheckResult{}, errors.New("native runner check job id is empty")
	}
	if j == nil || j.executor == nil {
		return nativeRunnerCheckResult{}, errors.New("native runner job executor is unavailable")
	}
	snapshot, err := j.executor.Watch(ctx, jobID)
	if err != nil {
		return nativeRunnerCheckResult{}, err
	}
	state := nativeRunnerJobState(snapshot.State)
	result := nativeRunnerCheckResult{State: state, Evidence: snapshot.Evidence}
	if snapshot.ExitCode != nil {
		result.ExitCode = *snapshot.ExitCode
	}
	if result.Evidence == "" && snapshot.Error != "" {
		result.Evidence = snapshot.Error
	}
	if result.State == "completed" && snapshot.ExitCode == nil {
		result.State = "failed"
		result.ExitCode = -1
		if result.Evidence == "" {
			result.Evidence = "completed job did not provide an exit code"
		} else {
			result.Evidence += "\ncompleted job did not provide an exit code"
		}
	}
	return result, nil
}

func nativeRunnerJobSpecFromCheck(request nativeRunnerCheckRequest) (nativeRunnerJobSpec, error) {
	key := strings.TrimSpace(request.IdempotencyKey)
	if len(key) < minNativeRunnerJobKeySize || len(key) > maxNativeRunnerJobKeySize {
		return nativeRunnerJobSpec{}, fmt.Errorf("check idempotency key must be %d to %d characters", minNativeRunnerJobKeySize, maxNativeRunnerJobKeySize)
	}
	if len(request.Check.Argv) == 0 || len(request.Check.Argv) > maxNativeRunnerJobArgCount {
		return nativeRunnerJobSpec{}, fmt.Errorf("check argv must contain 1 to %d items", maxNativeRunnerJobArgCount)
	}
	argv := make([]string, len(request.Check.Argv))
	totalBytes := 0
	for i, arg := range request.Check.Argv {
		if len(arg) == 0 || len(arg) > maxNativeRunnerJobArgBytes || strings.IndexByte(arg, 0) >= 0 {
			return nativeRunnerJobSpec{}, errors.New("check argv contains an invalid argument")
		}
		totalBytes += len(arg)
		if totalBytes > maxNativeRunnerJobArgTotal {
			return nativeRunnerJobSpec{}, errors.New("check argv exceeds total size limit")
		}
		argv[i] = arg
	}
	workingDirectory := strings.TrimSpace(request.WorkingDirectory)
	if workingDirectory == "" {
		return nativeRunnerJobSpec{}, errors.New("check working directory is empty")
	}
	base, err := filepath.Abs(workingDirectory)
	if err != nil {
		return nativeRunnerJobSpec{}, err
	}
	cwd := strings.TrimSpace(request.Check.Cwd)
	if cwd == "" {
		cwd = base
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(base, cwd)
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nativeRunnerJobSpec{}, err
	}
	if request.Check.TimeoutSeconds < 0 || request.Check.TimeoutSeconds > int(maxNativeRunnerJobTimeout/time.Second) {
		return nativeRunnerJobSpec{}, fmt.Errorf("check timeout exceeds %s", maxNativeRunnerJobTimeout)
	}
	timeout := time.Duration(request.Check.TimeoutSeconds) * time.Second
	return nativeRunnerJobSpec{Cwd: filepath.Clean(abs), Argv: argv, Runtime: "host", Timeout: timeout, IdempotencyKey: key}, nil
}

func nativeRunnerJobState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "canceled", "running", "unknown":
		return strings.ToLower(strings.TrimSpace(state))
	case "expired", "interrupted":
		return "failed"
	default:
		return "unknown"
	}
}
