//go:build windows

package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestExecutionRuntimeValidation(t *testing.T) {
	for _, tc := range []struct {
		input executionRuntime
		kind  string
		ok    bool
	}{
		{input: executionRuntime{}, kind: "host", ok: true},
		{input: executionRuntime{Kind: "HOST"}, kind: "host", ok: true},
		{input: executionRuntime{Kind: "wsl", Distribution: "Ubuntu-24.04"}, kind: "wsl", ok: true},
		{input: executionRuntime{Kind: "host", Distribution: "Ubuntu"}},
		{input: executionRuntime{Kind: "docker"}},
	} {
		got, err := normalizeExecutionRuntime(tc.input)
		if (err == nil) != tc.ok || (err == nil && got.Kind != tc.kind) {
			t.Fatalf("runtime=%+v got=%+v err=%v", tc.input, got, err)
		}
	}
	if _, _, _, err := prepareExecutionPlatform(context.Background(), `C:\`, []string{"wsl.exe", "true"}, executionRuntime{Kind: "wsl"}); err == nil {
		t.Fatal("runtime=wsl accepted nested wsl.exe argv")
	}
}

func TestRealWSLExecutionRuntimeAndTiming(t *testing.T) {
	if os.Getenv("FAST_SPIDER_WSL_E2E") != "1" {
		t.Skip("set FAST_SPIDER_WSL_E2E=1 to run real WSL execution")
	}
	base := os.Getenv("FAST_SPIDER_WSL_TEST_CWD")
	cwd := ""
	if base == "" {
		cwd = t.TempDir()
	} else {
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		var err error
		cwd, err = os.MkdirTemp(base, "Fast Spider 中文 ")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(cwd) })
	}
	resolved, err := ResolveMachinePath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	distribution := os.Getenv("FAST_SPIDER_WSL_DISTRIBUTION")
	runtimeSpec := executionRuntime{Kind: "wsl", Distribution: distribution}
	mapArgs := []string{}
	if distribution != "" {
		mapArgs = append(mapArgs, "--distribution", distribution)
	}
	mapArgs = append(mapArgs, "--exec", "wslpath", "-u", "--", resolved)
	expectedRaw, err := exec.Command("wsl.exe", mapArgs...).Output()
	if err != nil {
		t.Fatalf("independent wslpath mapping: %v", err)
	}
	expectedPWD := strings.TrimSpace(string(expectedRaw))
	if expectedPWD == "" || !strings.HasPrefix(expectedPWD, "/") {
		t.Fatalf("independent wslpath output=%q", expectedPWD)
	}
	manager := NewJobManager(t.TempDir())
	defer stopWSLKeepAlives()
	waitTerminal := func(jobID string) (JobSnapshot, error) {
		watchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cursor := int64(0)
		allEvents := []JobEvent{}
		for {
			snapshot, err := manager.Watch(watchCtx, jobID, cursor, 5*time.Second)
			if err != nil {
				return JobSnapshot{}, err
			}
			allEvents = append(allEvents, snapshot.Events...)
			cursor = snapshot.NextCursor
			if isTerminalJobState(snapshot.State) {
				snapshot.Events = allEvents
				return snapshot, nil
			}
		}
	}
	queueSamples := make([]int64, 0, 20)
	runSamples := make([]int64, 0, 20)
	for index := 0; index < 20; index++ {
		key := "wsl-e2e-runtime-" + time.Now().UTC().Format("150405.000000000")
		job, err := manager.StartExecution(context.Background(), resolved, []string{"/bin/sh", "-c", "printf 'wsl-ok:%s' \"$PWD\""}, runtimeSpec, 30*time.Second, key, "req_wsl_runtime_e2e", "tr_wsl_runtime_e2e")
		if err != nil {
			t.Fatal(err)
		}
		terminal, err := waitTerminal(job.JobID)
		if err != nil || terminal.State != "completed" || terminal.Runtime != "wsl" || terminal.RequestID == "" || terminal.TraceID == "" {
			t.Fatalf("terminal=%+v err=%v", terminal, err)
		}
		var output strings.Builder
		for _, event := range terminal.Events {
			if event.Type == "stdout" {
				output.WriteString(event.Text)
			}
		}
		actualPWD := strings.TrimSpace(strings.TrimPrefix(output.String(), "wsl-ok:"))
		if actualPWD != expectedPWD {
			t.Fatalf("WSL PWD=%q want exact mapped path %q (raw output=%q)", actualPWD, expectedPWD, output.String())
		}
		if terminal.Timing.QueueMs < 0 || terminal.Timing.RunMs < 0 || terminal.Timing.NodeReceivedAt == "" || terminal.Timing.ProcessStartedAt == "" {
			t.Fatalf("timing=%+v", terminal.Timing)
		}
		queueSamples = append(queueSamples, terminal.Timing.QueueMs)
		runSamples = append(runSamples, terminal.Timing.RunMs)
	}
	for index, smoke := range []struct {
		name string
		argv []string
		want string
	}{
		{name: "uname", argv: []string{"/bin/uname", "-a"}, want: "Linux"},
		{name: "go-version", argv: []string{"go", "version"}, want: "go version"},
		{name: "node-version", argv: []string{"node", "--version"}, want: "v"},
		{name: "go-build", argv: []string{"/bin/sh", "-c", "d=$(mktemp -d); printf 'package main\\nimport \"fmt\"\\nfunc main(){fmt.Print(\"wsl-build-ok\")}\\n' > \"$d/main.go\"; go run \"$d/main.go\"; rc=$?; rm -rf \"$d\"; exit $rc"}, want: "wsl-build-ok"},
	} {
		job, err := manager.StartExecution(context.Background(), resolved, smoke.argv, runtimeSpec, time.Minute, fmt.Sprintf("wsl-smoke-%02d", index), "req_wsl_smoke", "tr_wsl_smoke")
		if err != nil {
			t.Fatalf("%s start: %v", smoke.name, err)
		}
		terminal, err := waitTerminal(job.JobID)
		if err != nil || terminal.State != "completed" {
			t.Fatalf("%s terminal=%+v err=%v", smoke.name, terminal, err)
		}
		var output strings.Builder
		for _, event := range terminal.Events {
			if event.Type == "stdout" {
				output.WriteString(event.Text)
			}
		}
		if !strings.Contains(output.String(), smoke.want) {
			t.Fatalf("%s output=%q", smoke.name, output.String())
		}
	}
	sort.Slice(queueSamples, func(i, j int) bool { return queueSamples[i] < queueSamples[j] })
	sort.Slice(runSamples, func(i, j int) bool { return runSamples[i] < runSamples[j] })
	t.Logf("WSL timing n=20 queue p50=%dms p95=%dms max=%dms run p50=%dms p95=%dms max=%dms cwd=%s",
		queueSamples[9], queueSamples[18], queueSamples[19], runSamples[9], runSamples[18], runSamples[19], filepath.Clean(resolved))

	sentinelPath := filepath.Join(resolved, ".fast-spider-wsl-acceptance")
	t.Cleanup(func() { _ = os.Remove(sentinelPath) })
	fileJob, err := manager.StartExecution(context.Background(), resolved, []string{"/bin/sh", "-c", "printf 'wsl-file-ok' > .fast-spider-wsl-acceptance"}, runtimeSpec, 30*time.Second, "wsl-file-e2e-0001", "req_wsl_file_e2e", "tr_wsl_file_e2e")
	if err != nil {
		t.Fatal(err)
	}
	fileTerminal, err := waitTerminal(fileJob.JobID)
	if err != nil || fileTerminal.State != "completed" {
		t.Fatalf("file terminal=%+v err=%v", fileTerminal, err)
	}
	if sentinel, err := os.ReadFile(sentinelPath); err != nil || string(sentinel) != "wsl-file-ok" {
		t.Fatalf("Windows-side sentinel=%q err=%v", sentinel, err)
	}
	cancelJob, err := manager.StartExecution(context.Background(), resolved, []string{"/bin/sh", "-c", "sleep 60"}, runtimeSpec, 2*time.Minute, "wsl-cancel-e2e-01", "req_wsl_cancel_e2e", "tr_wsl_cancel_e2e")
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := manager.Cancel(context.Background(), cancelJob.JobID)
	if err != nil || canceled.State != "canceled" {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
}
