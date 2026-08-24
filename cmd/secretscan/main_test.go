package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTreeReturnsFindingWithoutCanary(t *testing.T) {
	root := t.TempDir()
	canary := "sk-" + strings.Repeat("Q8w7", 10)
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--tree", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, canary) {
		t.Fatal("CLI output disclosed a canary")
	}
	for _, field := range []string{"source=tree", "path=", "line=", "rule=openai-token"} {
		if !strings.Contains(combined, field) {
			t.Fatalf("CLI output missing %q: %s", field, combined)
		}
	}
}

func TestRunSyntheticSelfTest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--self-test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PASS") {
		t.Fatalf("stdout = %q, want PASS", stdout.String())
	}
}

func TestRunRedactsCanaryFromNestedZIPError(t *testing.T) {
	root := t.TempDir()
	canary := "ghp_" + strings.Repeat("N6m5", 10)
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create(canary + ".zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("malformed nested archive")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outer.zip"), archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--tree", root}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(stdout.String()+stderr.String(), canary) {
		t.Fatal("ZIP error output disclosed an entry-name canary")
	}
}

func TestRunRedactsPrivateMarkerFromMalformedZIPPath(t *testing.T) {
	root := t.TempDir()
	marker := "local-private-marker-" + strings.Repeat("J3", 8)
	markerFile := filepath.Join(t.TempDir(), "markers.txt")
	if err := os.WriteFile(markerFile, []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, marker+".zip"), []byte("malformed archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--tree", root, "--markers", markerFile}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(stdout.String()+stderr.String(), marker) {
		t.Fatal("CLI error output disclosed a private marker from a path")
	}
}

func TestRunRejectsConflictingModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--tree", ".", "--history"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestPublicExportInvokesTreeScannerOnFinalOutput(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "public-export.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	archive := strings.Index(text, `git archive --format=tar`)
	scan := strings.Index(text, `go run ./cmd/secretscan --tree "$output"`)
	commit := strings.Index(text, `git init -q`)
	if archive < 0 || scan < archive || commit < scan {
		t.Fatal("public export must scan the populated output tree before creating its public commit")
	}
}

func TestPublicExportRejectsSecretPresentOnlyInFilename(t *testing.T) {
	repo := t.TempDir()
	for _, relative := range []string{
		"cmd/secretscan/main.go",
		"internal/secretscan/format.go",
		"internal/secretscan/git.go",
		"internal/secretscan/rules.go",
		"internal/secretscan/scanner.go",
		"internal/secretscan/selftest.go",
		"scripts/public-export.sh",
	} {
		copyFixtureFile(t, filepath.Join("..", "..", filepath.FromSlash(relative)), filepath.Join(repo, filepath.FromSlash(relative)))
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/isguang2024/fast-spider\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canary := "glpat-" + strings.Repeat("K7m4", 10)
	if err := os.WriteFile(filepath.Join(repo, canary+".txt"), []byte("safe content"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureCommand(t, repo, "git", "init", "-q")
	runFixtureCommand(t, repo, "git", "config", "user.name", "Export Test")
	runFixtureCommand(t, repo, "git", "config", "user.email", "export@example.invalid")
	runFixtureCommand(t, repo, "git", "add", "-A")
	runFixtureCommand(t, repo, "git", "commit", "-q", "-m", "fixture")
	output := filepath.Join(t.TempDir(), "public")
	cmd := exec.Command("bash", "scripts/public-export.sh", "--output", output, "--skip-tests")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	combined, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("public export accepted a secret present only in a filename")
	}
	if strings.Contains(string(combined), canary) {
		t.Fatal("public export output disclosed a filename canary")
	}
}

func TestGitignoreCoversRootAndNestedCredentialStores(t *testing.T) {
	root := filepath.Join("..", "..")
	repo := t.TempDir()
	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), gitignore, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureCommand(t, repo, "git", "init", "-q")
	for _, path := range []string{
		".aws/credentials", "nested/.aws/credentials",
		".docker/config.json", "nested/.docker/config.json",
		".kube/config", "nested/.kube/config",
		".config/gcloud/application_default_credentials.json",
		"nested/.config/gcloud/application_default_credentials.json",
	} {
		cmd := exec.Command("git", "-C", repo, "check-ignore", "--no-index", "-q", "--", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("credential path %s is not ignored: %v (%s)", path, err, strings.TrimSpace(string(output)))
		}
	}
}

func copyFixtureFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func runFixtureCommand(t *testing.T, directory, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v (%s)", name, err, strings.TrimSpace(string(output)))
	}
}
