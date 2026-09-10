package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPersistedProcessIdentityUsesBirthAndFullPath(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("process recovery requires Windows or Linux process identity")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	identity := persistedProcessIdentityForCommand(&exec.Cmd{Path: executable, Process: process})
	if identity.Birth == "" {
		t.Fatal("process creation identity was not captured")
	}
	if state, err := persistedProcessStateOf(identity); err != nil || state != persistedProcessRunning {
		t.Fatalf("own identity: state=%v err=%v", state, err)
	}
	wrongPath := identity
	wrongPath.Path = filepath.Join(filepath.Dir(identity.Path), "another-directory", filepath.Base(identity.Path))
	if state, err := persistedProcessStateOf(wrongPath); state != persistedProcessUnknown || err == nil {
		t.Fatalf("same filename in a different directory must not authorize termination: state=%v err=%v", state, err)
	}
	reused := identity
	reused.Birth += "-another-process"
	if state, err := persistedProcessStateOf(reused); err != nil || state != persistedProcessStopped {
		t.Fatalf("a reused PID must release the old job without terminating its new owner: state=%v err=%v", state, err)
	}
}
