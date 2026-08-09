//go:build windows

package node

import (
	"path/filepath"
	"testing"
)

func TestNormalizeMachinePathInputDriveRootShorthand(t *testing.T) {
	if got := normalizeMachinePathInput(`V:`); got != `V:\` {
		t.Fatalf("normalize V: = %q, want V:\\", got)
	}
	if got := normalizeMachinePathInput(`v:`); got != `v:\` {
		t.Fatalf("normalize v: = %q, want v:\\", got)
	}
	if got := normalizeMachinePathInput(`V:folder`); got != `V:folder` {
		t.Fatalf("drive-relative path was unexpectedly normalized: %q", got)
	}
}

func TestResolveMachinePathAcceptsCurrentDriveRootShorthand(t *testing.T) {
	volume := filepath.VolumeName(t.TempDir())
	if len(volume) != 2 || volume[1] != ':' {
		t.Fatalf("unexpected Windows temp volume %q", volume)
	}
	resolved, err := ResolveMachinePath(volume)
	if err != nil {
		t.Fatalf("ResolveMachinePath(%q): %v", volume, err)
	}
	if filepath.VolumeName(resolved) != volume {
		t.Fatalf("resolved volume=%q want=%q path=%q", filepath.VolumeName(resolved), volume, resolved)
	}
}
