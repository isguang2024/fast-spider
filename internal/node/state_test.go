package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateSaveLoadAndValidation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "nested", "state.json")
	want := State{
		HubURL:         "https://hub.example",
		MachineID:      "mach_test",
		CredentialID:   "cred_test",
		HubPublicKey:   "public-key",
		HubFingerprint: "sha256:fingerprint",
	}
	if err := SaveState(statePath, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("loaded state=%+v, want %+v", got, want)
	}
	if _, err := os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary state file still exists: %v", err)
	}

	if err := os.WriteFile(statePath, []byte(`{"hubUrl":"https://hub.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(statePath); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete state error=%v", err)
	}
}
