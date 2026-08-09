package node_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
)

func TestNodeConnectUsesConnectionTokenWithoutOAuth(t *testing.T) {
	fixture := newNodeConnectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	token, err := fixture.service.CreateConnectionToken(ctx, fixture.account.OwnerID, "Windows Dev", 90*24*time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-connect-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Connect(ctx, fixture.httpServer.URL, token.Token, "Windows Dev")
	if err != nil {
		t.Fatal(err)
	}
	if state.MachineID == "" || state.HubURL != fixture.httpServer.URL {
		t.Fatalf("unexpected node state: %+v", state)
	}
	machines, err := fixture.service.ListMachines(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 1 || machines[0].MachineID != state.MachineID || machines[0].DisplayName != "Windows Dev" {
		t.Fatalf("unexpected machines: %+v", machines)
	}
	authorizations, err := fixture.st.ListOAuthAuthorizations(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 0 {
		t.Fatalf("Node connection created OAuth authorizations: %+v", authorizations)
	}
	clients, err := fixture.st.ListOAuthClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 0 {
		t.Fatalf("Node connection created OAuth clients: %+v", clients)
	}
}

func TestConnectionTokenCanRegisterMultipleIndependentNodes(t *testing.T) {
	fixture := newNodeConnectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	token, err := fixture.service.CreateConnectionToken(ctx, fixture.account.OwnerID, "Shared Device Token", 90*24*time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	firstClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-token-reuse-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	secondClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-token-reuse-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstClient.Connect(ctx, fixture.httpServer.URL, token.Token, "First Node")
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondClient.Connect(ctx, fixture.httpServer.URL, token.Token, "Second Node")
	if err != nil {
		t.Fatal(err)
	}
	if first.MachineID == second.MachineID {
		t.Fatalf("reused token returned the same machine ID %q", first.MachineID)
	}
	machines, err := fixture.service.ListMachines(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 {
		t.Fatalf("reused connection token registered %d machines, want 2", len(machines))
	}
	for _, machine := range machines {
		if machine.RegistrationMode != "connection_token" || machine.ConfigurationScope != "local_node" || machine.RuntimeCredential != "device_key" || machine.ConnectionTokenSaved {
			t.Fatalf("unexpected machine connection model: %+v", machine)
		}
	}
}

func TestNodeReconnectAfterRevokedMachinePreservesWorkspaceRegistry(t *testing.T) {
	fixture := newNodeConnectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nodeDataDir := t.TempDir()
	workspaceRoot := t.TempDir()
	workspace, err := node.NewWorkspaceStore(nodeDataDir).Add(workspaceRoot, "persistent workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(nodeDataDir, "workspaces.json")
	before, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.service.CreateConnectionToken(ctx, fixture.account.OwnerID, "Reconnect Token", 90*24*time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	nodeClient, err := node.New(node.Config{DataDir: nodeDataDir, Version: "node-reconnect-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := nodeClient.Connect(ctx, fixture.httpServer.URL, token.Token, "Repaired Node")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeMachine(ctx, fixture.account.OwnerID, first.MachineID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	second, err := nodeClient.Connect(ctx, fixture.httpServer.URL, token.Token, "Repaired Node Again")
	if err != nil {
		t.Fatal(err)
	}
	if second.MachineID == "" || second.MachineID == first.MachineID {
		t.Fatalf("reconnect did not create a new machine ID: first=%q second=%q", first.MachineID, second.MachineID)
	}
	after, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("workspaces.json changed during revoked-machine reconnect")
	}
	retained, err := node.NewWorkspaceStore(nodeDataDir).Lookup(workspace.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.WorkspaceID != workspace.WorkspaceID || retained.Root != workspace.Root {
		t.Fatalf("workspace identity was not retained: before=%+v after=%+v", workspace, retained)
	}
}

func TestRevokedConnectionTokenCannotRegisterNode(t *testing.T) {
	fixture := newNodeConnectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	token, err := fixture.service.CreateConnectionToken(ctx, fixture.account.OwnerID, "One Device", 90*24*time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeConnectionToken(ctx, fixture.account.OwnerID, token.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-revoked-token-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeClient.Connect(ctx, fixture.httpServer.URL, token.Token, "Denied Node"); err == nil {
		t.Fatal("revoked connection token unexpectedly registered a node")
	}
	machines, err := fixture.service.ListMachines(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 0 {
		t.Fatalf("revoked connection token created machines: %+v", machines)
	}
}

type nodeConnectFixture struct {
	st         *store.Store
	service    *core.Service
	account    core.OwnerAccountView
	httpServer *httptest.Server
}

func newNodeConnectFixture(t *testing.T) nodeConnectFixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "node-connect-fixture"})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "node-owner", "Node Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = st.Close()
	})
	return nodeConnectFixture{st: st, service: service, account: account, httpServer: httpServer}
}
