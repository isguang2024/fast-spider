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

func TestEnrollmentDeviceTokenAndRevocation(t *testing.T) {
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
	owner, err := service.BootstrapOwner(ctx, bootstrap, "Owner", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := service.AuthenticateOwner(ctx, owner.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.CreateEnrollmentToken(ctx, ownerID, "test-node", "windows", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := EnrollRequest{
		EnrollmentToken: enrollment.EnrollmentToken,
		IdempotencyKey:  "idem_test_enrollment_0001",
		DisplayName:     "test-node",
		OS:              "windows",
		Arch:            "amd64",
		NodeVersion:     "test",
		PublicKey:       security.EncodePublicKey(publicKey),
	}

	results := make(chan EnrollResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Enroll(ctx, req, "127.0.0.1")
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent idempotent enroll failed: %v", err)
		}
	}
	var machineID string
	for result := range results {
		if machineID == "" {
			machineID = result.MachineID
		}
		if result.MachineID != machineID {
			t.Fatalf("idempotent enroll returned different machines: %q != %q", result.MachineID, machineID)
		}
	}

	conflict := req
	conflict.IdempotencyKey = "idem_test_enrollment_0002"
	if _, err := service.Enroll(ctx, conflict, "127.0.0.1"); !errors.Is(err, store.ErrConsumed) {
		t.Fatalf("second idempotency key after consume error=%v, want ErrConsumed", err)
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
	for i, item := range wanted {
		enrollment, err := service.CreateEnrollmentToken(ctx, account.OwnerID, item.name, "linux", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		publicKey, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Enroll(ctx, EnrollRequest{
			EnrollmentToken: enrollment.EnrollmentToken,
			IdempotencyKey:  "capability_batch_idempotency_" + string(rune('a'+i)),
			DisplayName:     item.name,
			OS:              "linux",
			Arch:            "amd64",
			NodeVersion:     "test",
			PublicKey:       security.EncodePublicKey(publicKey),
		}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if result.MachineID == "" {
			t.Fatalf("enrollment %s returned empty machine ID", item.name)
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
}

func stringJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
