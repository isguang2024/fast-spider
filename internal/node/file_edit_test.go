package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func boolPtr(value bool) *bool { return &value }

func editParams(path string, extra map[string]any) map[string]any {
	params := map[string]any{"path": path}
	for key, value := range extra {
		params[key] = value
	}
	return params
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFileEditCreateRequiresAbsenceAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	client := &Client{}
	result, err := client.fileEdit(context.Background(), "create", editParams(path, map[string]any{"content": "hello\n", "expectedAbsent": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !result.Changed || result.OldSHA256 != "" || result.NewSHA256 != sha256String([]byte("hello\n")) || string(readTestFile(t, path)) != "hello\n" {
		t.Fatalf("create result=%+v content=%q", result, readTestFile(t, path))
	}
	if result.LineDelta != 1 {
		t.Fatalf("create lineDelta=%d want=1", result.LineDelta)
	}
	if _, err := client.fileEdit(context.Background(), "create", editParams(path, map[string]any{"content": "overwrite", "expectedAbsent": true})); !errors.Is(err, ErrFileAlreadyExists) {
		t.Fatalf("existing create error=%v", err)
	}
	if got := string(readTestFile(t, path)); got != "hello\n" {
		t.Fatalf("existing create changed file: %q", got)
	}
	missingParent := filepath.Join(dir, "missing", "file.txt")
	if _, err := client.fileEdit(context.Background(), "create", editParams(missingParent, map[string]any{"content": "x", "expectedAbsent": true})); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent error=%v", err)
	}
	if _, err := client.fileEdit(context.Background(), "create", editParams(filepath.Join(dir, "no-flag.txt"), map[string]any{"content": "x"})); err == nil {
		t.Fatal("create accepted missing expectedAbsent")
	}
}

func TestFileEditCanonicalLockCoversSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	alias := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	realLockTarget, err := resolveFileEditLockTarget(target, "replace")
	if err != nil {
		t.Fatal(err)
	}
	aliasLockTarget, err := resolveFileEditLockTarget(alias, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if fileEditLock(realLockTarget) != fileEditLock(aliasLockTarget) {
		t.Fatalf("real and symlink aliases selected different locks: %q / %q", realLockTarget, aliasLockTarget)
	}
}

func TestFileEditReplaceCASUniqueAndLegacyEntry(t *testing.T) {
	dir := t.TempDir()
	client := &Client{}
	for _, action := range []string{"replace", "edit"} {
		path := filepath.Join(dir, action+".txt")
		before := []byte("alpha\nold\nomega")
		if err := os.WriteFile(path, before, 0o640); err != nil {
			t.Fatal(err)
		}
		result, err := client.fileEdit(context.Background(), action, editParams(path, map[string]any{"oldText": "old", "newText": "new", "expectedFileSha256": sha256String(before)}))
		if err != nil {
			t.Fatal(err)
		}
		if !result.Success || !result.Changed || result.OldSHA256 != sha256String(before) || result.NewSHA256 != sha256String([]byte("alpha\nnew\nomega")) || result.Diff != "" {
			t.Fatalf("%s result=%+v", action, result)
		}
	}
	path := filepath.Join(dir, "negative.txt")
	original := []byte("same same")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, old, hash string
		want            error
	}{
		{"cas", "same", sha256String([]byte("stale")), ErrRevisionConflict},
		{"duplicate", "same", sha256String(original), ErrEditNotUnique},
		{"zero", "missing", sha256String(original), ErrEditNotUnique},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.fileEdit(context.Background(), "replace", editParams(path, map[string]any{"oldText": tc.old, "newText": "x", "expectedFileSha256": tc.hash}))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if !bytes.Equal(readTestFile(t, path), original) {
				t.Fatal("failed replace changed file")
			}
		})
	}
}

func TestFileEditManyUsesOriginalRangesAndIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	client := &Client{}
	path := filepath.Join(dir, "many.txt")
	original := []byte("alpha beta gamma")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.fileEdit(context.Background(), "editMany", editParams(path, map[string]any{
		"expectedFileSha256": sha256String(original),
		"edits":              []map[string]any{{"oldText": "alpha", "newText": "A"}, {"oldText": "gamma", "newText": "G"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.EditsApplied != 2 || result.Diff != "" || string(readTestFile(t, path)) != "A beta G" {
		t.Fatalf("result=%+v", result)
	}

	for name, edits := range map[string][]map[string]any{
		"one-invalid": {{"oldText": "A", "newText": "alpha"}, {"oldText": "missing", "newText": "x"}},
		"overlap":     {{"oldText": "A beta", "newText": "x"}, {"oldText": "beta G", "newText": "y"}},
	} {
		t.Run(name, func(t *testing.T) {
			before := readTestFile(t, path)
			_, err := client.fileEdit(context.Background(), "editMany", editParams(path, map[string]any{"expectedFileSha256": sha256String(before), "edits": edits}))
			if name == "overlap" && !errors.Is(err, ErrEditOverlap) {
				t.Fatalf("error=%v", err)
			}
			if name == "one-invalid" && !errors.Is(err, ErrEditNotUnique) {
				t.Fatalf("error=%v", err)
			}
			if !bytes.Equal(readTestFile(t, path), before) {
				t.Fatal("invalid editMany changed file")
			}
		})
	}
}

func TestFileEditPreviewReusesPlannerWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	client := &Client{}
	path := filepath.Join(dir, "preview.txt")
	before := []byte("one two three\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	for _, tc := range []struct {
		name, previewOf string
		params          map[string]any
		want            []byte
	}{
		{"replace", "replace", map[string]any{"oldText": "two", "newText": "2", "expectedFileSha256": sha256String(before)}, []byte("one 2 three\n")},
		{"many", "editMany", map[string]any{"edits": []map[string]any{{"oldText": "one", "newText": "1"}, {"oldText": "three", "newText": "3"}}, "expectedFileSha256": sha256String(before)}, []byte("1 two 3\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.params["previewOf"] = tc.previewOf
			result, err := client.fileEdit(context.Background(), "preview", editParams(path, tc.params))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed || !result.Preview || result.Operation != tc.previewOf || result.NewSHA256 != sha256String(tc.want) || result.Diff == "" {
				t.Fatalf("result=%+v", result)
			}
			if !bytes.Equal(readTestFile(t, path), before) {
				t.Fatal("preview wrote file")
			}
		})
	}
	created := filepath.Join(dir, "new.txt")
	result, err := client.fileEdit(context.Background(), "preview", editParams(created, map[string]any{"previewOf": "create", "content": "new", "expectedAbsent": true}))
	if err != nil || !result.Changed || !result.Preview || result.NewSHA256 != sha256String([]byte("new")) {
		t.Fatalf("create preview=%+v err=%v", result, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("create preview created file")
	}
	afterInfo, _ := os.Stat(path)
	if !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("preview changed mtime")
	}
}

func TestFileEditNoOpPreservesMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noop.txt")
	before := []byte("same")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	result, err := (&Client{}).fileEdit(context.Background(), "replace", editParams(path, map[string]any{"oldText": "same", "newText": "same", "expectedFileSha256": sha256String(before)}))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if result.Changed || !info.ModTime().Equal(oldTime) {
		t.Fatalf("result=%+v mtime=%v", result, info.ModTime())
	}
}

func TestFileEditMutationResponseIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "one-line", old: "secret-old", new: "secret-new"},
		{name: "fifty-lines", old: strings.Repeat("secret-old\n", 50), new: strings.Repeat("secret-new\n", 50)},
		{name: "large-file", old: "secret-target", new: "secret-result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".txt")
			padding := ""
			if tc.name == "large-file" {
				padding = strings.Repeat("padding-line\n", 30000)
			}
			before := []byte(padding + tc.old + "\n")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := (&Client{}).fileEdit(context.Background(), "replace", editParams(path, map[string]any{"oldText": tc.old, "newText": tc.new, "expectedFileSha256": sha256String(before)}))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) >= 1024 || bytes.Contains(raw, []byte("secret-old")) || bytes.Contains(raw, []byte("secret-new")) || bytes.Contains(raw, []byte(`"diff"`)) {
				t.Fatalf("mutation response is not lean: bytes=%d response=%s", len(raw), raw)
			}
		})
	}
}

func TestFileEditPreservesBOMNewlinesTailAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "style.txt")
	before := append([]byte{0xef, 0xbb, 0xbf}, []byte("first\r\nold\r\nlast")...)
	if err := os.WriteFile(path, before, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := (&Client{}).fileEdit(context.Background(), "replace", editParams(path, map[string]any{"oldText": "old", "newText": "middle\nadded", "expectedFileSha256": sha256String(before)}))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xef, 0xbb, 0xbf}, []byte("first\r\nmiddle\r\nadded\r\nlast")...)
	if got := readTestFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("content=%q want=%q", got, want)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("mode=%o", info.Mode().Perm())
		}
	}
}

func TestFileEditAtomicReplacementAndTempCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomicEditedFile(path, []byte("after"), 0o600, sha256String([]byte("before"))); err != nil {
		t.Fatal(err)
	}
	if string(readTestFile(t, path)) != "after" {
		t.Fatal("atomic replacement failed")
	}
	if err := writeAtomicEditedFile(path, []byte("bad"), 0o600, sha256String([]byte("stale"))); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("error=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".fast-spider-edit-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files=%v err=%v", matches, err)
	}
	if string(readTestFile(t, path)) != "after" {
		t.Fatal("failed atomic write changed target")
	}
}

func TestFileEditBoundsAndDiffTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.txt")
	before := []byte("needle")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	tooLarge := strings.Repeat("x", maxEditTextBytes+1)
	if _, err := (&Client{}).fileEdit(context.Background(), "replace", editParams(path, map[string]any{"oldText": "needle", "newText": tooLarge, "expectedFileSha256": sha256String(before)})); err == nil {
		t.Fatal("oversized edit accepted")
	}
	large := strings.Repeat("x", maxEditTextBytes)
	diff, truncated := boundedEditDiff("file.txt", "replace", []fileTextEdit{{OldText: large, NewText: large}})
	if !truncated || len(diff) > maxReturnedDiffBytes || !utf8.ValidString(diff) {
		t.Fatalf("diff len=%d truncated=%v", len(diff), truncated)
	}
	edits := make([]map[string]any, maxFileEdits+1)
	for i := range edits {
		edits[i] = map[string]any{"oldText": "needle", "newText": "x"}
	}
	if _, err := (&Client{}).fileEdit(context.Background(), "editMany", editParams(path, map[string]any{"edits": edits, "expectedFileSha256": sha256String(before)})); err == nil {
		t.Fatal("too many edits accepted")
	}
}
