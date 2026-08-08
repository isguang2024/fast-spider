package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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
