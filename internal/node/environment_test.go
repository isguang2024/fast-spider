package node

import (
	"os"
	"strings"
	"testing"
)

func TestSafeShellEnvironmentExcludesUnapprovedSecrets(t *testing.T) {
	t.Setenv("FAST_SPIDER_TEST_SECRET", "do-not-forward")
	t.Setenv("OPENAI_API_KEY", "do-not-forward")
	t.Setenv("PATH", os.Getenv("PATH"))

	env := safeShellEnvironment()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if strings.Contains(joined, "FAST_SPIDER_TEST_SECRET=") || strings.Contains(joined, "OPENAI_API_KEY=") {
		t.Fatalf("safe shell environment leaked a non-allowlisted secret: %v", env)
	}
	if os.Getenv("PATH") != "" && !strings.Contains(strings.ToUpper(joined), "\nPATH=") {
		t.Fatalf("safe shell environment omitted PATH: %v", env)
	}
}
