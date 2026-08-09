package nodeinstance

import (
	"errors"
	"testing"
)

func TestLeaseIsExclusiveAndReacquirable(t *testing.T) {
	dataDir := t.TempDir()
	first, err := Acquire(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Acquire(dataDir)
	if !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second Acquire error=%v, want ErrAlreadyRunning", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
