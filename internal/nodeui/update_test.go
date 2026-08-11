package nodeui

import (
	"errors"
	"reflect"
	"testing"
)

func TestStartupUpdateMaintenanceAppliesBeforeConsumedCurrentCleanup(t *testing.T) {
	t.Parallel()
	var calls []string
	applied, err := runStartupUpdateMaintenance(func() (bool, error) {
		calls = append(calls, "apply-ready")
		return false, nil
	}, func() error {
		calls = append(calls, "cleanup-consumed-current")
		return nil
	})
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	want := []string{"apply-ready", "cleanup-consumed-current"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestStartupUpdateMaintenanceDoesNotCleanupAfterReadyError(t *testing.T) {
	t.Parallel()
	readyErr := errors.New("ready failed")
	cleanupCalled := false
	applied, err := runStartupUpdateMaintenance(func() (bool, error) {
		return false, readyErr
	}, func() error {
		cleanupCalled = true
		return nil
	})
	if applied || !errors.Is(err, readyErr) {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if cleanupCalled {
		t.Fatal("consumed-current cleanup ran after Ready/apply error")
	}
}

func TestStartupUpdateMaintenanceDoesNotCleanupAfterApplyStarts(t *testing.T) {
	t.Parallel()
	cleanupCalled := false
	applied, err := runStartupUpdateMaintenance(func() (bool, error) {
		return true, nil
	}, func() error {
		cleanupCalled = true
		return nil
	})
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if cleanupCalled {
		t.Fatal("consumed-current cleanup ran after update apply started")
	}
}
