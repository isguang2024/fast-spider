package releaseinfo_test

import (
	"testing"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

func TestManifestRejectsUnsafeReleaseVersion(t *testing.T) {
	manifest := releaseinfo.NewManifest("node", "fast-spider-node", "windows-amd64", "0.2.0-../../escape", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10, "/download")
	if err := manifest.Validate(); err == nil {
		t.Fatal("unsafe release version was accepted")
	}
}

func TestCompareAllowsCurrentDevelopmentSuffix(t *testing.T) {
	comparison, err := releaseinfo.Compare("0.1.0-dev", "0.2.0")
	if err != nil || comparison >= 0 {
		t.Fatalf("compare=%d err=%v", comparison, err)
	}
}
