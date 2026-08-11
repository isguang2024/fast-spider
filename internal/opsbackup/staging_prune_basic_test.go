package opsbackup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func mkStage(t *testing.T, r, n string, fs map[string]string) string {
	t.Helper()
	d := filepath.Join(r, n)
	if e := os.MkdirAll(d, 0700); e != nil {
		t.Fatal(e)
	}
	for q, c := range fs {
		p := filepath.Join(d, filepath.FromSlash(q))
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(c), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return d
}
func sNames(x []StagingPruneItem) []string {
	r := make([]string, 0, len(x))
	for _, v := range x {
		r = append(r, v.BaseName)
	}
	return r
}
func TestStagingPruneStrictNamesAndOptions(t *testing.T) {
	r := t.TempDir()
	cs := []struct {
		l StagingLayout
		n string
		w bool
	}{{StagingLayoutLocal, "release-0.4.6", true}, {StagingLayoutLocal, "release-0.4.6-abcdef0", true}, {StagingLayoutLocal, "release-01.4.6", false}, {StagingLayoutLocal, "fast-spider-0.4.6", false}, {StagingLayoutServer, "fast-spider-0.4.6", true}, {StagingLayoutServer, "fast-spider-deploy-x", false}}
	for _, c := range cs {
		if g := stagingNamePatterns[c.l].MatchString(c.n); g != c.w {
			t.Fatalf("%s=%v", c.n, g)
		}
	}
	bad := []StagingPruneOptions{{Directory: "relative", Layout: StagingLayoutLocal, ThroughVersion: "0.4.6"}, {Directory: r, Layout: "bad", ThroughVersion: "0.4.6"}, {Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "01.4.6"}}
	for _, o := range bad {
		if _, e := PruneReleaseStaging(context.Background(), o); e == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}
func TestPruneReleaseStagingPlanLocal(t *testing.T) {
	r := t.TempDir()
	mkStage(t, r, "release-0.4.4", map[string]string{"a": "1"})
	mkStage(t, r, "release-0.4.6-abcdef0", map[string]string{"b": "22"})
	mkStage(t, r, "release-0.4.7-deadbee", map[string]string{"c": "333"})
	os.Mkdir(filepath.Join(r, "release-latest"), 0700)
	os.WriteFile(filepath.Join(r, "release-0.4.3"), []byte("file"), 0600)
	x, e := PruneReleaseStaging(context.Background(), StagingPruneOptions{Directory: r, Layout: StagingLayoutLocal, ThroughVersion: "0.4.6"})
	if e != nil {
		t.Fatal(e)
	}
	if x.CandidateCount != 3 || x.PlannedCount != 2 || x.RetainedCount != 1 || x.DeletedCount != 0 {
		t.Fatalf("%+v", x)
	}
	if !slices.Equal(sNames(x.Planned), []string{"release-0.4.4", "release-0.4.6-abcdef0"}) {
		t.Fatal(x.Planned)
	}
}
func TestPruneReleaseStagingApplyServerIdempotent(t *testing.T) {
	r := t.TempDir()
	mkStage(t, r, "fast-spider-0.4.4", map[string]string{"a": "1"})
	mkStage(t, r, "fast-spider-0.4.5-abcdef0", map[string]string{"b": "22"})
	mkStage(t, r, "fast-spider-0.4.7-deadbee", map[string]string{"c": "333"})
	u := filepath.Join(r, "fast-spider-deploy-old")
	os.Mkdir(u, 0700)
	o := StagingPruneOptions{Directory: r, Layout: StagingLayoutServer, ThroughVersion: "0.4.5", Apply: true}
	x, e := PruneReleaseStaging(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if x.DeletedCount != 2 || x.RetainedCount != 1 {
		t.Fatalf("%+v", x)
	}
	for _, n := range []string{"fast-spider-0.4.4", "fast-spider-0.4.5-abcdef0"} {
		if _, e := os.Stat(filepath.Join(r, n)); !os.IsNotExist(e) {
			t.Fatalf("remains %s", n)
		}
	}
	for _, p := range []string{filepath.Join(r, "fast-spider-0.4.7-deadbee"), u} {
		if _, e := os.Stat(p); e != nil {
			t.Fatal(e)
		}
	}
	y, e := PruneReleaseStaging(context.Background(), o)
	if e != nil || y.DeletedCount != 0 || y.PlannedCount != 0 || y.RetainedCount != 1 {
		t.Fatalf("%+v %v", y, e)
	}
}
func TestPruneReleaseStagingInjectedReparseZeroDelete(t *testing.T) {
	for _, m := range []string{"root", "candidate", "nested"} {
		t.Run(m, func(t *testing.T) {
			r := t.TempDir()
			c := mkStage(t, r, "release-0.4.6", map[string]string{"x": "data"})
			n := filepath.Join(c, "x")
			d := stageDeps()
			d.isReparse = func(p string, i os.FileInfo) (bool, error) {
				if (m == "root" && p == r) || (m == "candidate" && p == c) || (m == "nested" && p == n) {
					return true, nil
				}
				return releaseBackupPathIsReparse(p, i)
			}
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
func stageDeps() stagingPruneDependencies {
	return stagingPruneDependencies{lstat: os.Lstat, readDir: os.ReadDir, isReparse: releaseBackupPathIsReparse, remove: os.Remove, limits: stagingScanLimits{maxFiles: MaxStagingPruneFiles, maxBytes: MaxStagingPruneBytes, maxDepth: MaxStagingPruneDepth}}
}
