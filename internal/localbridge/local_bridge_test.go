//go:build localbridgee2e

package localbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestLocalBridgeTransportRoundTripAndValidation(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "fs-lb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)
	otherDataDir, err := os.MkdirTemp("", "fs-lb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(otherDataDir)
	if Endpoint(dataDir) != Endpoint(dataDir) || Endpoint(dataDir) == Endpoint(otherDataDir) {
		t.Fatalf("unexpected endpoint scoping: %q %q", Endpoint(dataDir), Endpoint(otherDataDir))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, dataDir, func(_ context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
			return protocolv1.CapabilityResponse{
				MessageType: protocolv1.MessageCapabilityResponse,
				RequestId:   req.RequestId,
				Result:      map[string]any{"capability": req.Capability, "action": req.Action},
			}
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("local bridge did not stop after cancellation")
		}
	}()

	var response protocolv1.CapabilityResponse
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		callCtx, callCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		response, err = Call(callCtx, dataDir, protocolv1.CapabilityRequest{Capability: "file.read", Action: "read"})
		callCancel()
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestId == "" || response.Result["capability"] != "file.read" {
		t.Fatalf("round trip response=%+v", response)
	}

	conn, err := dialLocalBridge(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, raw := range []string{
		`{"messageType":"capability.request","capability":"file.read","action":"read","unknown":true}`,
		`{"messageType":"wrong","requestId":"bad_type","capability":"file.read","action":"read"}`,
	} {
		response := writeAndReadLine(t, conn, raw)
		if response.Error == nil || response.Error.Code != "INVALID_REQUEST" {
			t.Fatalf("invalid request response=%+v", response)
		}
	}
	if _, err := conn.Write(append(bytes.Repeat([]byte("x"), maxLocalBridgeMessageBytes+1), '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("oversized request unexpectedly received a response")
	}
}

func TestLocalBridgeCancellationClosesIdleConnectionsBeforeRunReturns(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "fs-lb-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	accepted := make(chan struct{}, 1)
	server := &localBridgeServer{sem: make(chan struct{}, 1)}
	go func() {
		done <- runLocalBridgeServer(ctx, dataDir, func(parent context.Context, conn io.ReadWriteCloser) {
			accepted <- struct{}{}
			server.serveConnection(parent, conn)
		})
	}()

	var conn io.ReadWriteCloser
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		conn, err = dialLocalBridge(context.Background(), dataDir)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("dial idle connection: %v", err)
	}
	defer conn.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("local bridge did not accept the idle connection")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return after canceling an idle connection")
	}
}

func writeAndReadLine(t *testing.T, conn io.ReadWriteCloser, raw string) protocolv1.CapabilityResponse {
	t.Helper()
	if _, err := conn.Write(append([]byte(raw), '\n')); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("bridge did not return a response: %v", scanner.Err())
	}
	var response protocolv1.CapabilityResponse
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.MessageType) == "" {
		t.Fatal("bridge response omitted message type")
	}
	return response
}
