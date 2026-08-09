package nodeinstance

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLeaseIsExclusiveAndReacquirable(t *testing.T) {
	oldResolver := instanceLockPath
	lockDir := t.TempDir()
	instanceLockPath = func() (string, error) { return filepath.Join(lockDir, "node.lock"), nil }
	t.Cleanup(func() { instanceLockPath = oldResolver })

	first, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Acquire()
	if !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second Acquire error=%v, want ErrAlreadyRunning", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
