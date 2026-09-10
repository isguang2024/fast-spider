package agent

import (
	"context"
	"testing"
	"time"
)

func TestNativeRunnerRateLimitDoesNotMultiplyHistoricalFailures(t *testing.T) {
	for _, test := range []struct {
		name, retry string
		delay       time.Duration
		failures    int
	}{
		{"server_deadline", "30", 30 * time.Second, 8},
		{"server_long_deadline", "300", 5 * time.Minute, 8},
		{"missing_deadline", "", 2 * time.Minute, 8},
		{"first_missing_deadline", "", 30 * time.Second, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _, p := newNativeRunnerForTest(t, "rate limit", nil)
			addNativeTask(t, r, p.ID, "limited", "scope")
			task := loadNativeTask(t, r, p.ID, "limited")
			task.Failures = test.failures
			now := time.Unix(1789069000, 0)
			r.now = func() time.Time { return now }
			r.fail(context.Background(), &task, &chatGPTCloudHTTPError{status: 429, retryAfter: test.retry})
			want := now.Add(test.delay).Unix()
			if task.NextAt != want || r.cooldownUntil != want {
				t.Fatalf("next=%d cooldown=%d want=%d", task.NextAt, r.cooldownUntil, want)
			}
		})
	}
}

func TestNativeRunnerReadRateLimitOnlyDelaysTask(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "read rate limit", nil)
	addNativeTask(t, r, p.ID, "read-limited", "scope")
	task := loadNativeTask(t, r, p.ID, "read-limited")
	now := time.Unix(1789069000, 0)
	r.now = func() time.Time { return now }

	r.fail(context.Background(), &task, &chatGPTCloudHTTPError{
		operation:  "read conversation",
		status:     429,
		retryAfter: "120",
	})

	want := now.Add(120 * time.Second).Unix()
	if task.NextAt != want {
		t.Fatalf("next=%d want=%d", task.NextAt, want)
	}
	if r.cooldownUntil != 0 {
		t.Fatalf("read rate limit set global cooldown=%d", r.cooldownUntil)
	}
}

func TestNativeRunnerWriteRateLimitSetsGlobalCooldown(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "write rate limit", nil)
	addNativeTask(t, r, p.ID, "write-limited", "scope")
	task := loadNativeTask(t, r, p.ID, "write-limited")
	now := time.Unix(1789069000, 0)
	r.now = func() time.Time { return now }

	r.fail(context.Background(), &task, &chatGPTCloudHTTPError{
		operation:  "conversation stream",
		status:     429,
		retryAfter: "120",
	})

	want := now.Add(120 * time.Second).Unix()
	if task.NextAt != want || r.cooldownUntil != want {
		t.Fatalf("next=%d cooldown=%d want=%d", task.NextAt, r.cooldownUntil, want)
	}
}
