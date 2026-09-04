package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestMachineRegistrationDeviceTokenAndRevocation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil || bootstrap == "" {
		t.Fatalf("EnsureBootstrap() token=%q err=%v", bootstrap, err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "owner", "Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ownerID := account.OwnerID

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := MachineRegistrationRequest{
		DisplayName: "test-node",
		OS:          "windows",
		Arch:        "amd64",
		NodeVersion: "test",
		PublicKey:   security.EncodePublicKey(publicKey),
	}

	results := make(chan MachineRegistrationResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.RegisterMachine(ctx, ownerID, req, "127.0.0.1")
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent idempotent registration failed: %v", err)
		}
	}
	var machineID string
	for result := range results {
		if machineID == "" {
			machineID = result.MachineID
		}
		if result.MachineID != machineID {
			t.Fatalf("idempotent registration returned different machines: %q != %q", result.MachineID, machineID)
		}
	}

	timestamp := protocolv1.Timestamp(time.Now())
	tokenReq := DeviceTokenRequest{
		MachineID: machineID,
		Nonce:     "nonce_test_1234567890",
		Timestamp: timestamp,
	}
	tokenReq.Signature = security.EncodeSignature(ed25519.Sign(privateKey, protocolv1.DeviceTokenPayload(tokenReq.MachineID, tokenReq.Nonce, tokenReq.Timestamp)))
	tokenResult, err := service.IssueDeviceToken(ctx, tokenReq, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateDevice(ctx, tokenResult.DeviceToken); err != nil {
		t.Fatalf("AuthenticateDevice() error=%v", err)
	}
	if _, err := service.IssueDeviceToken(ctx, tokenReq, "127.0.0.1"); !errors.Is(err, store.ErrReplay) {
		t.Fatalf("replayed nonce error=%v, want ErrReplay", err)
	}

	firstGeneration, err := st.NextGeneration(ctx, machineID, time.Now())
	if err != nil || firstGeneration != 1 {
		t.Fatalf("first generation=%d err=%v", firstGeneration, err)
	}
	secondGeneration, err := st.NextGeneration(ctx, machineID, time.Now())
	if err != nil || secondGeneration != 2 {
		t.Fatalf("second generation=%d err=%v", secondGeneration, err)
	}

	if err := service.RevokeMachine(ctx, ownerID, machineID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateDevice(ctx, tokenResult.DeviceToken); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("revoked device token error=%v, want ErrRevoked", err)
	}
}

func TestCapabilityCatalogIncludesBrowser(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	foundBrowser := false
	foundScreenshot := false
	for _, capability := range service.CapabilityCatalog() {
		if capability.CapabilityId == protocolv1.BrowserCapability.CapabilityId {
			foundBrowser = true
		}
		if capability.CapabilityId == protocolv1.ScreenshotCapability.CapabilityId {
			foundScreenshot = true
			if stringJSON(capability.Actions) != stringJSON(protocolv1.ScreenshotCapability.Actions) {
				t.Fatalf("Hub screenshot catalog actions=%v want=%v", capability.Actions, protocolv1.ScreenshotCapability.Actions)
			}
		}
	}
	if !foundBrowser {
		t.Fatal("Hub capability catalog omitted browser.automation")
	}
	if !foundScreenshot {
		t.Fatal("Hub capability catalog omitted screenshot.capture")
	}
}

func TestScreenshotWindowCallDeadlineAndAuditPolicy(t *testing.T) {
	if got := capabilityCallTimeout("screenshot.capture", "window"); got != 2*time.Minute {
		t.Fatalf("screenshot window deadline=%s, want 2m", got)
	}
	for _, action := range []string{"desktop", "display", "window"} {
		if !shouldAuditCapability("screenshot.capture", action) {
			t.Fatalf("screenshot.capture/%s is not audited", action)
		}
	}
	if shouldAuditCapability("screenshot.capture", "listWindows") {
		t.Fatal("screenshot.capture/listWindows should not create a capture audit entry")
	}
}

func TestAgentCapabilityRetryAndAuditPolicy(t *testing.T) {
	for _, action := range []string{"routing.status", "provider.capabilities", "skills.list", "hooks.list", "permissions.list", "plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list", "session.create", "session.callback.enqueue", "session.callback.list", "session.goal.get"} {
		if !isRetryableCapability("agent.control", action) {
			t.Fatalf("%s should be retryable", action)
		}
	}
	for _, action := range []string{"session.send", "session.steer", "session.respond", "session.callback.register", "session.callback.arm", "session.callback.unregister", "session.callback.claim", "session.callback.ack", "session.delete", "session.rollback", "session.settings.update", "session.review"} {
		if isRetryableCapability("agent.control", action) {
			t.Fatalf("%s must not be retryable", action)
		}
	}
	if !shouldAuditCapability("agent.control", "session.delete") || !shouldAuditCapability("agent.control", "session.goal.set") || !shouldAuditCapability("agent.control", "session.steer") || !shouldAuditCapability("agent.control", "session.respond") || !shouldAuditCapability("agent.control", "session.callback.register") || !shouldAuditCapability("agent.control", "session.callback.arm") || !shouldAuditCapability("agent.control", "session.callback.enqueue") || !shouldAuditCapability("agent.control", "session.callback.unregister") || !shouldAuditCapability("agent.control", "session.callback.claim") || !shouldAuditCapability("agent.control", "session.callback.ack") {
		t.Fatal("destructive/state-changing agent actions must be audited")
	}
	if shouldAuditCapability("agent.control", "skills.list") {
		t.Fatal("skill discovery should not be audited as a mutation")
	}
	if !shouldAuditCapability("build.exec", "run") {
		t.Fatal("build.exec/run must be audited as a mutation")
	}
}

func TestSessionCreateSeparatesOperationAndResponseDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
	operationDeadline, responseDeadline := capabilityCallDeadlines(now, context.Background(), "agent.control", "session.create")
	if got := operationDeadline.Sub(now); got != 150*time.Second {
		t.Fatalf("session.create operation deadline=%s, want 2m30s", got)
	}
	if got := responseDeadline.Sub(operationDeadline); got != 20*time.Second {
		t.Fatalf("session.create response grace=%s, want 20s", got)
	}

	callerDeadline := now.Add(40 * time.Second)
	callerCtx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()
	operationDeadline, responseDeadline = capabilityCallDeadlines(now, callerCtx, "agent.control", "session.create")
	if !operationDeadline.Equal(callerDeadline) || !responseDeadline.Equal(callerDeadline) {
		t.Fatalf("caller deadline was extended: operation=%s response=%s caller=%s", operationDeadline, responseDeadline, callerDeadline)
	}
}

func TestWorkingContextRetryAndAuditPolicy(t *testing.T) {
	if !isRetryableCapability("working.context", "get") {
		t.Fatal("working.context/get should be safely retryable")
	}
	if shouldAuditCapability("working.context", "get") {
		t.Fatal("working.context/get must not be audited as a mutation")
	}
	for _, action := range []string{"set", "clear"} {
		if isRetryableCapability("working.context", action) {
			t.Fatalf("working.context/%s must not be retryable", action)
		}
		if !shouldAuditCapability("working.context", action) {
			t.Fatalf("working.context/%s must be audited", action)
		}
	}
	for _, removed := range []string{"plan.init", "plan.get", "task.update", "markdown.append", "progress.watch"} {
		if isRetryableCapability("working.context", removed) || shouldAuditCapability("working.context", removed) {
			t.Fatalf("removed working.context action %s still has transport policy", removed)
		}
	}
}

func TestFileEditRetryAndAuditPolicy(t *testing.T) {
	if !isRetryableCapability("file.write", "preview") {
		t.Fatal("file.write/preview should be safely retryable")
	}
	if shouldAuditCapability("file.write", "preview") {
		t.Fatal("file.write/preview must not be audited as a mutation")
	}
	for _, action := range []string{"edit", "create", "replace", "editMany"} {
		if isRetryableCapability("file.write", action) {
			t.Fatalf("file.write/%s must not be retryable", action)
		}
		if !shouldAuditCapability("file.write", action) {
			t.Fatalf("file.write/%s must be audited", action)
		}
	}
}

func TestListMachinesLoadsCapabilitiesForOwnerBatch(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "capability-batch-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "cap-owner", "Capability Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	wanted := []struct {
		name string
		id   string
	}{
		{name: "Node A", id: "capability-a"},
		{name: "Node B", id: "capability-b"},
	}
	for _, item := range wanted {
		publicKey, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.RegisterMachine(ctx, account.OwnerID, MachineRegistrationRequest{
			DisplayName: item.name,
			OS:          "linux",
			Arch:        "amd64",
			NodeVersion: "test",
			PublicKey:   security.EncodePublicKey(publicKey),
		}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if result.MachineID == "" {
			t.Fatalf("registration %s returned empty machine ID", item.name)
		}
		if err := st.ReplaceCapabilities(ctx, result.MachineID, []protocolv1.CapabilityDescriptor{{
			CapabilityId: "file.read",
			Version:      "1.0",
			Actions:      []string{"read", "list"},
		}}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}

	byMachine, err := st.CapabilitiesByOwner(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byMachine) != len(wanted) {
		t.Fatalf("CapabilitiesByOwner returned %d machines, want %d: %+v", len(byMachine), len(wanted), byMachine)
	}
	machines, err := service.ListMachines(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != len(wanted) {
		t.Fatalf("ListMachines returned %d machines, want %d: %+v", len(machines), len(wanted), machines)
	}
	for _, machine := range machines {
		if len(machine.Capabilities) != 1 || machine.Capabilities[0].CapabilityId != "file.read" || len(machine.Capabilities[0].Actions) != 2 {
			t.Fatalf("machine %s capabilities=%+v", machine.MachineID, machine.Capabilities)
		}
	}
	firstPage, hasMore, err := service.ListMachinesPage(ctx, account.OwnerID, 0, 1, false)
	if err != nil || len(firstPage) != 1 || !hasMore || firstPage[0].DisplayName != "Node A" || len(firstPage[0].Capabilities) != 0 {
		t.Fatalf("compact first machine page=%+v hasMore=%v err=%v", firstPage, hasMore, err)
	}
	secondPage, hasMore, err := service.ListMachinesPage(ctx, account.OwnerID, 1, 1, false)
	if err != nil || len(secondPage) != 1 || hasMore || secondPage[0].DisplayName != "Node B" || len(secondPage[0].Capabilities) != 0 {
		t.Fatalf("compact second machine page=%+v hasMore=%v err=%v", secondPage, hasMore, err)
	}
	fullPage, _, err := service.ListMachinesPage(ctx, account.OwnerID, 0, 1, true)
	if err != nil || len(fullPage) != 1 || len(fullPage[0].Capabilities) != 1 {
		t.Fatalf("full machine page=%+v err=%v", fullPage, err)
	}
}

func stringJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestCapabilityTransportErrorsAreStructured(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		capability string
		action     string
		wantCode   string
		retryable  bool
		status     int
	}{
		{name: "lost read", err: registry.ErrConnectionLost, capability: "file.read", action: "read", wantCode: "CONNECTION_LOST", retryable: true, status: 503},
		{name: "lost edit", err: registry.ErrConnectionLost, capability: "file.write", action: "edit", wantCode: "CONNECTION_LOST", retryable: false, status: 503},
		{name: "lost browser action", err: registry.ErrConnectionLost, capability: "browser.automation", action: "click", wantCode: "CONNECTION_LOST", retryable: false, status: 503},
		{name: "lost agent send", err: registry.ErrConnectionLost, capability: "agent.control", action: "session.send", wantCode: "CONNECTION_LOST", retryable: false, status: 503},
		{name: "deadline query", err: context.DeadlineExceeded, capability: "git.repository", action: "status", wantCode: "DEADLINE_EXCEEDED", retryable: true, status: 504},
		{name: "deadline shell", err: context.DeadlineExceeded, capability: "shell.exec", action: "run", wantCode: "DEADLINE_EXCEEDED", retryable: false, status: 504},
		{name: "deadline idempotent create", err: context.DeadlineExceeded, capability: "agent.control", action: "session.create", wantCode: "DEADLINE_EXCEEDED", retryable: true, status: 504},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := capabilityCallTransportError(tt.err, tt.capability, tt.action)
			var callErr *CapabilityCallError
			if !errors.As(err, &callErr) {
				t.Fatalf("error=%T %v, want CapabilityCallError", err, err)
			}
			if callErr.Code != tt.wantCode || callErr.Retryable != tt.retryable {
				t.Fatalf("call error=%+v, want code=%s retryable=%v", callErr, tt.wantCode, tt.retryable)
			}
			if got := ErrorStatus(err); got != tt.status {
				t.Fatalf("ErrorStatus()=%d, want %d", got, tt.status)
			}
			if tt.action == "session.create" {
				if callErr.Details["recovery"] != "retry_same_idempotency_key" || callErr.Details["mayHaveCreated"] != true {
					t.Fatalf("session.create recovery details=%#v", callErr.Details)
				}
			}
		})
	}
}
