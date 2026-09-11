package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type archiveTestBackend struct {
	*fakeNativeRunnerBackend
	calls []string
	err   error
}

func (b *archiveTestBackend) ArchiveSession(_ context.Context, t nativeRunnerTask) error {
	b.calls = append(b.calls, t.Receipt.SessionID)
	return b.err
}

func archiveTask(id, state string) nativeRunnerTask {
	return nativeRunnerTask{ID: id, Round: 2, State: state, Receipt: &nativeRunnerReceipt{SessionID: id, Generation: 2}, Result: &nativeRunnerResult{Terminal: true}, ResultAcked: true}
}

func TestNativeArchiveEligibilityAndReusedSession(t *testing.T) {
	task := archiveTask("current", "returned")
	task.History = []nativeRunnerAttempt{{Round: 1, Receipt: task.Receipt, Result: task.Result, Acked: true}}
	if len(nativeArchiveCandidates(task)) != 0 {
		t.Fatal("unaccepted or reused session was archived")
	}
	task.History[0].Receipt = &nativeRunnerReceipt{SessionID: "old", Generation: 1}
	if got := nativeArchiveCandidates(task); len(got) != 1 || got[0].Receipt.SessionID != "old" {
		t.Fatal("completed old generation not selected")
	}
	task.State = "accepted"
	if len(nativeArchiveCandidates(task)) != 2 {
		t.Fatal("accepted current and old generation not selected")
	}
	task.ResultAcked = false
	if len(nativeArchiveCandidates(task)) != 1 {
		t.Fatal("unacknowledged current selected")
	}
}

func TestNativeArchivePreparedReuseAndInflightDrain(t *testing.T) {
	r, base, p := newNativeRunnerForTest(t, "archive", nil)
	b := &archiveTestBackend{fakeNativeRunnerBackend: base, err: errNativeRunnerOperationPending}
	r.backend = b
	task := addNativeTask(t, r, p.ID, "work", "")
	task.State = "accepted"
	task.Receipt = &nativeRunnerReceipt{SessionID: "cloud", Generation: 1}
	task.Result = &nativeRunnerResult{Terminal: true}
	task.ResultAcked = true
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{task}); err != nil {
		t.Fatal(err)
	}
	got := loadNativeTask(t, r, p.ID, task.ID)
	if r.closeoutTask == nil || got.SessionCloseout[0].State != "archiving" {
		t.Fatal("pending archive not retained")
	}
	got.History = []nativeRunnerAttempt{{Round: 1, Receipt: got.Receipt, Result: got.Result, Acked: true}}
	got.Receipt = nil
	got.Result = nil
	got.State = "queued"
	if len(nativeArchiveCandidates(got)) != 0 {
		t.Fatal("next round's reusable session selected")
	}
	b.err = nil
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{got}); err != nil {
		t.Fatal(err)
	}
	got = loadNativeTask(t, r, p.ID, task.ID)
	if r.closeoutTask != nil || got.State != "queued" || got.SessionCloseout[0].State != "archived" {
		t.Fatal("changed task stranded housekeeping or was overwritten")
	}
	got.History[0].GoalVersion = p.GoalVersion
	req, err := r.compile(p, got, []nativeRunnerTask{got})
	if err != nil || !req.RestoreArchived || req.TargetSessionID != "cloud" {
		t.Fatalf("archived reuse not restored: %+v %v", req, err)
	}
}

func TestNativeArchiveTransportWaitsForJobsWithoutCancelling(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method %s", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["is_archived"] != true {
			t.Error("not an online archive")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	m.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	m.chatgptCloud.baseURL = server.URL
	m.chatgptCloud.http = server.Client()
	executor := &stopJobExecutor{watch: nativeRunnerJobSnapshot{JobID: "job", State: "running"}}
	tr := &nativeRunnerTransport{manager: m, jobs: nativeRunnerJobStore{executor: executor}, dataDir: t.TempDir()}
	defer tr.closeJobs()
	task := archiveTask("cloud", "accepted")
	task.Recovery = &nativeRunnerRecovery{Checkpoint: nativeRunnerCheckpoint{WaitingJobs: []string{"job"}}}
	if err := tr.ArchiveSession(context.Background(), task); err == nil || requests != 0 {
		t.Fatal("active job archived")
	}
	executor.watch = nativeRunnerJobSnapshot{JobID: "job", State: "completed", ExitCode: intPtr(0)}
	if err := tr.ArchiveSession(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(executor.cancelled) != 0 {
		t.Fatal("archive read conversation or cancelled job")
	}
}

func TestNativeArchiveDoesNotChangeReviewEvidenceOrBlockBusiness(t *testing.T) {
	r, base, p := newNativeRunnerForTest(t, "archive", nil)
	b := &archiveTestBackend{fakeNativeRunnerBackend: base, err: &chatGPTCloudHTTPError{status: 429, retryAfter: "600"}}
	r.backend = b
	task := addNativeTask(t, r, p.ID, "work", "")
	task.State = "accepted"
	task.Receipt = &nativeRunnerReceipt{SessionID: "cloud", Generation: 1}
	task.Result = &nativeRunnerResult{Terminal: true}
	task.ResultAcked = true
	if err := r.saveTask(context.Background(), task, "test"); err != nil {
		t.Fatal(err)
	}
	basis := nativeBasis(task)
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{task}); err != nil {
		t.Fatal(err)
	}
	got := loadNativeTask(t, r, p.ID, task.ID)
	if got.State != "accepted" || nativeHolds(got) || nativeBasis(got) != basis || r.cooldownUntil != 0 {
		t.Fatal("archive changed business state or evidence")
	}
	if len(got.SessionCloseout) != 1 || got.SessionCloseout[0].State != "retry" || got.SessionCloseout[0].NextAt < r.now().Add(599*time.Second).Unix() {
		t.Fatal("missing retry-after")
	}
	r.closeoutNextAt = 0
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{got}); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || r.closeoutNextAt == 0 {
		t.Fatal("restart lost archive cooldown")
	}
	b.err = nil
	r.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{got}); err != nil {
		t.Fatal(err)
	}
	got = loadNativeTask(t, r, p.ID, task.ID)
	if got.SessionCloseout[0].State != "archived" {
		t.Fatal("retry did not finish")
	}
	if err := r.tickSessionCloseout(context.Background(), []nativeRunnerTask{got}); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 2 {
		t.Fatal("archived session requested again")
	}
}
