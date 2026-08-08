package node

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzGitRefAndPathValidation(f *testing.F) {
	for _, seed := range []string{"main", "feature/test", "../outside", "-option", "a\x00b", "a/b", `C:\\temp`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		_ = validateGitRef(value)
		paths, err := validateGitPaths([]string{value})
		if err != nil {
			return
		}
		if len(paths) != 1 {
			t.Fatalf("validateGitPaths returned %d paths", len(paths))
		}
		accepted := filepath.FromSlash(paths[0])
		if filepath.IsAbs(accepted) || filepath.VolumeName(accepted) != "" || accepted == ".." || strings.HasPrefix(accepted, ".."+string(filepath.Separator)) || strings.ContainsRune(accepted, 0) {
			t.Fatalf("validateGitPaths accepted unsafe output %q from %q", paths[0], value)
		}
	})
}
