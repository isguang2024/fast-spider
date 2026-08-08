package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/adminclient"
	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPhase1EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}

	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	bootstrapClient, err := adminclient.New(httpServer.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := bootstrapClient.Bootstrap(ctx, bootstrapToken, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := adminclient.New(httpServer.URL, owner.OwnerToken, true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := admin.CreateEnrollment(ctx, "test-node", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}

	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Enroll(ctx, httpServer.URL, enrollment.EnrollmentToken, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan error, 1)
	go func() { nodeDone <- nodeClient.Run(runCtx) }()
	defer func() {
		stopNode()
		select {
		case <-nodeDone:
		case <-time.After(3 * time.Second):
			t.Error("node did not stop after context cancellation")
		}
	}()

	waitFor(t, 5*time.Second, func() bool {
		machines, err := admin.ListMachines(ctx)
		return err == nil && len(machines) == 1 && machines[0].MachineID == state.MachineID && machines[0].Online
	})

	mcpHTTP := &http.Client{Transport: bearerTransport{token: owner.OwnerToken, base: http.DefaultTransport}}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "phase1-e2e", Version: "test"}, nil)
	mcpSession, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		HTTPClient:           mcpHTTP,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSession.Close()

	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	wantNames := []string{"capability_list", "machine_get", "machine_list"}
	if stringJSON(names) != stringJSON(wantNames) {
		t.Fatalf("MCP tools=%v want=%v", names, wantNames)
	}

	callResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "machine_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(callResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var machineList struct {
		Machines []struct {
			MachineID string `json:"machineId"`
			Online    bool   `json:"online"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(raw, &machineList); err != nil {
		t.Fatalf("decode MCP machine_list result: %v raw=%s", err, raw)
	}
	if len(machineList.Machines) != 1 || machineList.Machines[0].MachineID != state.MachineID || !machineList.Machines[0].Online {
		t.Fatalf("unexpected MCP machine list: %s", raw)
	}

	if err := admin.RevokeMachine(ctx, state.MachineID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		machines, err := admin.ListMachines(ctx)
		return err == nil && len(machines) == 1 && machines[0].Status == "revoked" && !machines[0].Online
	})
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func stringJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
