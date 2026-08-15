package opsbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPruneReleaseStagingLimitsZeroDelete(t *testing.T) {
	cases := []struct {
		n  string
		fs map[string]string
		l  stagingScanLimits
	}{{"files", map[string]string{"a": "1", "b": "2"}, stagingScanLimits{1, 1024, 8}}, {"bytes", map[string]string{"a": "ab"}, stagingScanLimits{10, 1, 8}}, {"depth", map[string]string{"a/b/c": "x"}, stagingScanLimits{10, 1024, 1}}}
	for _, q := range cases {
		t.Run(q.n, func(t *testing.T) {
			r := t.TempDir()
			c := mkStage(t, r, "release-0.4.6", q.fs)
			d := stageDeps()
			d.limits = q.l
			x, e := pruneReleaseStaging(context.Background(), StagingPruneOptions{Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "0.4.6", Apply: true}, d)
			if e == nil || x.DeletedCount != 0 {
				t.Fatalf("%+v %v", x, e)
			}
			if _, e := os.Stat(c); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestPruneReleaseStagingCandidateLimitZeroDelete(t *testing.T) {
	r := t.TempDir()
	for i := 0; i <= MaxStagingPruneCandidates; i++ {
		n := fmt.Sprintf("release-0.4.%d", i)
		if e := os.Mkdir(filepath.Join(r, n), 0700); e != nil {
			t.Fatal(e)
		}
	}
	x, e := PruneReleaseStaging(context.Background(), StagingPruneOptions{Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "0.4.999", Apply: true})
	if e == nil || x.DeletedCount != 0 {
		t.Fatalf("%+v %v", x, e)
	}
	a, _ := os.ReadDir(r)
	if len(a) != MaxStagingPruneCandidates+1 {
		t.Fatalf("entries=%d", len(a))
	}
}
func TestPruneReleaseStagingTOCTOUZeroDelete(t *testing.T) {
	r := t.TempDir()
	c := mkStage(t, r, "release-0.4.6", map[string]string{"x": "old"})
	p := filepath.Join(c, "x")
	d := stageDeps()
	d.beforeRecheck = func() {
		if e := os.WriteFile(p, []byte("changed-after-plan"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	x, e := pruneReleaseStaging(context.Background(), StagingPruneOptions{Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "0.4.6", Apply: true}, d)
	if e == nil || x.DeletedCount != 0 {
		t.Fatalf("%+v %v", x, e)
	}
	if _, e := os.Stat(c); e != nil {
		t.Fatal(e)
	}
}

func TestPruneReleaseStagingIsolationPreservesConcurrentReplacement(t *testing.T) {
	root := t.TempDir()
	candidate := mkStage(t, root, "release-0.4.6", map[string]string{"x": "old"})
	deps := stageDeps()
	originalRename := deps.rename
	var injected bool
	deps.rename = func(oldPath, newPath string) error {
		if err := originalRename(oldPath, newPath); err != nil {
			return err
		}
		if !injected && oldPath == candidate {
			injected = true
			if err := os.MkdirAll(candidate, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(candidate, "new"), []byte("new release"), 0o600)
		}
		return nil
	}
	result, err := pruneReleaseStaging(context.Background(), StagingPruneOptions{
		Directory: root, Layout: StagingLayoutLocal, ThroughVersion: "0.4.6", Apply: true,
	}, deps)
	if err != nil || result.DeletedCount != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, err := os.ReadFile(filepath.Join(candidate, "new"))
	if err != nil || string(raw) != "new release" {
		t.Fatalf("concurrent replacement was removed or changed: raw=%q err=%v", raw, err)
	}
}

func TestPruneReleaseStagingIsolationRejectsIdentitySwap(t *testing.T) {
	root := t.TempDir()
	candidate := mkStage(t, root, "release-0.4.6", map[string]string{"x": "old"})
	preservedOriginal := candidate + ".planned"
	deps := stageDeps()
	originalRename := deps.rename
	var injected bool
	deps.rename = func(oldPath, newPath string) error {
		if !injected && oldPath == candidate {
			injected = true
			if err := originalRename(candidate, preservedOriginal); err != nil {
				return err
			}
			if err := os.MkdirAll(candidate, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(candidate, "fresh"), []byte("fresh release"), 0o600); err != nil {
				return err
			}
		}
		return originalRename(oldPath, newPath)
	}
	result, err := pruneReleaseStaging(context.Background(), StagingPruneOptions{
		Directory: root, Layout: StagingLayoutLocal, ThroughVersion: "0.4.6", Apply: true,
	}, deps)
	if err == nil || result.DeletedCount != 0 {
		t.Fatalf("identity swap result=%+v err=%v", result, err)
	}
	if raw, readErr := os.ReadFile(filepath.Join(candidate, "fresh")); readErr != nil || string(raw) != "fresh release" {
		t.Fatalf("swapped candidate was deleted: raw=%q err=%v", raw, readErr)
	}
	if raw, readErr := os.ReadFile(filepath.Join(preservedOriginal, "x")); readErr != nil || string(raw) != "old" {
		t.Fatalf("planned candidate was not preserved: raw=%q err=%v", raw, readErr)
	}
}
func TestPruneReleaseStagingPartialDeleteFacts(t *testing.T) {
	r := t.TempDir()
	f := mkStage(t, r, "release-0.4.4", map[string]string{"x": "fail"})
	ok := mkStage(t, r, "release-0.4.5", map[string]string{"x": "ok"})
	boom := errors.New("denied")
	d := stageDeps()
	d.remove = func(p string) error {
		if filepath.Base(filepath.Dir(p)) == filepath.Base(f) {
			return boom
		}
		return os.Remove(p)
	}
	x, e := pruneReleaseStaging(context.Background(), StagingPruneOptions{Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "0.4.5", Apply: true}, d)
	if !errors.Is(e, boom) || x.DeletedCount != 1 || x.RetainedCount != 1 {
		t.Fatalf("%+v %v", x, e)
	}
	if _, e := os.Stat(f); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(ok); !os.IsNotExist(e) {
		t.Fatalf("ok remains %v", e)
	}
	if !slices.Contains(sNames(x.Deleted), "release-0.4.5") || !slices.Contains(sNames(x.Retained), "release-0.4.4") {
		t.Fatalf("facts %+v", x)
	}
}
