package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestRegisterGenerationAndSnapshotIsolation(t *testing.T) {
	r := New()
	now := time.Now()
	first := &Connection{MachineID: "mach_test", ConnectionID: "conn_1", Generation: 1, ConnectedAt: now, LastSeenAt: now, Status: "ready"}
	if replaced, accepted := r.Register(first); replaced != nil || !accepted {
		t.Fatalf("first register=(%v, %v)", replaced, accepted)
	}
	if _, accepted := r.Register(&Connection{MachineID: first.MachineID, Generation: 1}); accepted {
		t.Fatal("registry accepted a duplicate generation")
	}
	if _, accepted := r.Register(&Connection{MachineID: first.MachineID, Generation: 0}); accepted {
		t.Fatal("registry accepted an older generation")
	}
	second := &Connection{MachineID: first.MachineID, ConnectionID: "conn_2", Generation: 2, ConnectedAt: now, LastSeenAt: now, Status: "ready"}
	if replaced, accepted := r.Register(second); replaced != first || !accepted {
		t.Fatalf("replacement register=(%v, %v)", replaced, accepted)
	}

	caps := []protocolv1.CapabilityDescriptor{{CapabilityId: "machine.status", Actions: []string{"report"}}}
	if !r.SetCapabilities(second.MachineID, second.Generation, caps) {
		t.Fatal("SetCapabilities rejected current generation")
	}
	snapshot, ok := r.Get(second.MachineID)
	if !ok {
		t.Fatal("Get did not find registered connection")
	}
	snapshot.Capabilities[0].Actions[0] = "mutated"
	current, _ := r.Get(second.MachineID)
	if current.Capabilities[0].Actions[0] != "report" {
		t.Fatal("registry snapshot exposed mutable capability state")
	}
	if r.Touch(second.MachineID, 1, "stale", now.Add(time.Minute)) {
		t.Fatal("Touch accepted an older generation")
	}
	r.Remove(second.MachineID, 1)
	if _, ok := r.Get(second.MachineID); !ok {
		t.Fatal("Remove removed the current connection using an older generation")
	}
	r.Remove(second.MachineID, 2)
	if _, ok := r.Get(second.MachineID); ok {
		t.Fatal("Remove left the current connection registered")
	}
}

func TestPendingCallReturnsWhenConnectionEnds(t *testing.T) {
	tests := []struct {
		name string
		end  func(*Registry, *Connection)
	}{
		{name: "close", end: func(_ *Registry, conn *Connection) {
			go func() { _ = conn.Close(websocket.StatusNormalClosure, "test close") }()
		}},
		{name: "remove", end: func(registry *Registry, conn *Connection) {
			registry.Remove(conn.MachineID, conn.Generation)
		}},
		{name: "replacement", end: func(registry *Registry, conn *Connection) {
			if replaced, accepted := registry.Register(&Connection{MachineID: conn.MachineID, Generation: conn.Generation + 1}); !accepted || replaced != conn {
				t.Fatalf("replacement register=(%v, %v)", replaced, accepted)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, requestRead := newWebSocketConnection(t)
			registry := New()
			if _, accepted := registry.Register(conn); !accepted {
				t.Fatal("connection was not registered")
			}

			callErr := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, err := registry.Call(ctx, conn.MachineID, protocolv1.CapabilityRequest{
					MessageType: protocolv1.MessageCapabilityRequest,
					RequestId:   "req_pending_" + tt.name,
					Capability:  "file.read",
					Action:      "read",
				})
				callErr <- err
			}()

			select {
			case <-requestRead:
			case <-time.After(2 * time.Second):
				t.Fatal("server did not receive pending request")
			}
			tt.end(registry, conn)

			select {
			case err := <-callErr:
				if !errors.Is(err, ErrConnectionLost) {
					t.Fatalf("Call() error=%v, want ErrConnectionLost", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending Call() did not return promptly")
			}
		})
	}
}

func TestPendingCallAcceptsResponseAfterOperationDeadlineWithinResponseGrace(t *testing.T) {
	requestRead := make(chan protocolv1.CapabilityRequest, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("Accept() error=%v", err)
			return
		}
		defer conn.CloseNow()
		var request protocolv1.CapabilityRequest
		if err := wsjson.Read(context.Background(), conn, &request); err != nil {
			t.Errorf("Read() error=%v", err)
			return
		}
		requestRead <- request
		<-release
	}))
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		close(release)
		server.Close()
		t.Fatal(err)
	}
	defer func() {
		wsConn.CloseNow()
		close(release)
		server.Close()
	}()

	conn := NewConnection("mach_late_success", "conn_late_success", 1, time.Now(), wsConn)
	registry := New()
	if _, accepted := registry.Register(conn); !accepted {
		t.Fatal("connection was not registered")
	}
	operationDeadline := time.Now().Add(25 * time.Millisecond)
	request := protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   "req_late_success_001",
		Capability:  "agent.control",
		Action:      "session.create",
		Params:      map[string]any{"idempotencyKey": "late-success-key-001"},
		Deadline:    protocolv1.Timestamp(operationDeadline),
	}
	go func() {
		observed := <-requestRead
		deadline, _ := time.Parse(time.RFC3339Nano, observed.Deadline)
		time.Sleep(time.Until(deadline) + 5*time.Millisecond)
		conn.DeliverResponse(protocolv1.CapabilityResponse{
			MessageType: protocolv1.MessageCapabilityResponse,
			RequestId:   observed.RequestId,
			Result: map[string]any{
				"sessionId":         "cloud-late-success",
				"phase":             "created_execution_unknown",
				"idempotencyStatus": "created",
			},
			Timestamp: protocolv1.Timestamp(time.Now()),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	response, err := registry.Call(ctx, conn.MachineID, request)
	if err != nil {
		t.Fatalf("late recovered response was lost: %v", err)
	}
	if time.Now().Before(operationDeadline) {
		t.Fatal("response arrived before the simulated operation deadline")
	}
	if response.Result["sessionId"] != "cloud-late-success" || response.Result["phase"] != "created_execution_unknown" {
		t.Fatalf("late response=%#v", response.Result)
	}
}

func newWebSocketConnection(t *testing.T) (*Connection, <-chan struct{}) {
	t.Helper()
	requestRead := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("Accept() error=%v", err)
			return
		}
		defer conn.CloseNow()
		var request protocolv1.CapabilityRequest
		if err := wsjson.Read(context.Background(), conn, &request); err != nil {
			t.Errorf("Read() error=%v", err)
			return
		}
		requestRead <- struct{}{}
		<-release
	}))
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		close(release)
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		wsConn.CloseNow()
		close(release)
		server.Close()
	})
	return NewConnection("mach_pending", "conn_pending", 1, time.Now(), wsConn), requestRead
}
