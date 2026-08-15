package node

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type runTestAgent struct {
	closeCalls atomic.Int32
}

func (a *runTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (a *runTestAgent) Close(context.Context) error {
	a.closeCalls.Add(1)
	return nil
}

func TestRunRejectsInsecureStoredHubByDefault(t *testing.T) {
	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	state := State{
		HubURL:         "http://127.0.0.1:8787",
		MachineID:      "mach_test",
		CredentialID:   "cred_test",
		HubPublicKey:   "unused",
		HubFingerprint: "unused",
	}
	if err := SaveState(filepath.Join(dataDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	err = client.Run(context.Background())
	if err == nil {
		t.Fatal("Run() accepted insecure stored Hub URL without AllowInsecure")
	}
	if !strings.Contains(err.Error(), "hub URL must use https") {
		t.Fatalf("Run() error=%q, want https enforcement", err)
	}
}

func TestRunHonorsAgentCallerOwnership(t *testing.T) {
	for _, test := range []struct {
		name          string
		callerOwned   bool
		wantCloseCall int32
	}{
		{name: "client owned", wantCloseCall: 1},
		{name: "caller owned", callerOwned: true, wantCloseCall: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			agent := &runTestAgent{}
			client, err := New(Config{
				DataDir:          dataDir,
				Version:          "agent-ownership-test",
				Agent:            agent,
				AgentCallerOwned: test.callerOwned,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveState(filepath.Join(dataDir, "state.json"), State{
				HubURL:         "http://127.0.0.1:8787",
				MachineID:      "mach_agent_ownership",
				CredentialID:   "cred_agent_ownership",
				HubPublicKey:   "unused",
				HubFingerprint: "unused",
			}); err != nil {
				t.Fatal(err)
			}

			if err := client.Run(context.Background()); err == nil {
				t.Fatal("Run() unexpectedly accepted insecure stored Hub URL")
			}
			if got := agent.closeCalls.Load(); got != test.wantCloseCall {
				t.Fatalf("agent Close() calls=%d, want %d", got, test.wantCloseCall)
			}
		})
	}
}

func TestReconnectBackoffsResetAfterStableSession(t *testing.T) {
	delay, next := reconnectBackoffs(maxReconnectBackoff, stableSessionDuration)
	if delay != initialReconnectBackoff || next != 2*initialReconnectBackoff {
		t.Fatalf("stable reconnect backoffs=(%s, %s)", delay, next)
	}

	delay, next = reconnectBackoffs(4*time.Second, stableSessionDuration-time.Second)
	if delay != 4*time.Second || next != 8*time.Second {
		t.Fatalf("short reconnect backoffs=(%s, %s)", delay, next)
	}

	delay, next = reconnectBackoffs(maxReconnectBackoff, time.Second)
	if delay != maxReconnectBackoff || next != maxReconnectBackoff {
		t.Fatalf("capped reconnect backoffs=(%s, %s)", delay, next)
	}
}

func TestHeartbeatLoopCancelsSessionRequestsButLeavesJobsRunning(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "session-cancel-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.jobs.CancelAll(context.Background()) }()
	job, err := client.jobs.StartShell(t.TempDir(), shellSleepArgv(), 30*time.Second, "idem_session_cancel_001")
	if err != nil {
		t.Fatal(err)
	}

	requestSent := make(chan struct{}, 1)
	stopServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("Accept() error=%v", err)
			return
		}
		defer conn.CloseNow()
		err = wsjson.Write(context.Background(), conn, protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_session_cancel",
			Capability:  "job.control",
			Action:      "watch",
			Params:      map[string]any{"jobId": job.JobID, "cursor": 1, "waitSeconds": 15},
			Deadline:    protocolv1.Timestamp(time.Now().Add(time.Minute)),
		})
		if err != nil {
			t.Errorf("Write() error=%v", err)
			return
		}
		requestSent <- struct{}{}
		<-stopServer
	}))
	stop := func() {
		select {
		case <-stopServer:
		default:
			close(stopServer)
		}
	}
	t.Cleanup(func() {
		stop()
		server.Close()
	})

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	loopErr := make(chan error, 1)
	go func() { loopErr <- client.heartbeatLoop(context.Background(), conn, time.Hour) }()

	select {
	case <-requestSent:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not send capability request")
	}
	waitForRequestSlots(t, client, 1)
	stop()
	select {
	case <-loopErr:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat loop did not end after disconnect")
	}
	waitForRequestSlots(t, client, 0)

	snapshot, err := client.jobs.Watch(context.Background(), job.JobID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "running" {
		t.Fatalf("job state=%q, want running after session cancellation", snapshot.State)
	}
}

func waitForRequestSlots(t *testing.T, client *Client, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.requestSem) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request semaphore usage=%d, want %d", len(client.requestSem), want)
}
