package secretscan

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

func TestScanRepositoryCoversTrackedUntrackedAndStagedOnly(t *testing.T) {
	repo := newGitRepository(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "safe\n")
	git(t, repo, nil, "add", "tracked.txt")
	git(t, repo, nil, "commit", "-m", "seed")

	trackedCanary := secretCanary("sk-")
	untrackedCanary := secretCanary("ghp_")
	stagedCanary := secretCanary("ctk_")
	writeFile(t, filepath.Join(repo, "tracked.txt"), trackedCanary+"\n")
	writeFile(t, filepath.Join(repo, "untracked.bin"), "\x00"+untrackedCanary)
	writeFile(t, filepath.Join(repo, "staged-only.txt"), stagedCanary+"\n")
	git(t, repo, nil, "add", "staged-only.txt")
	writeFile(t, filepath.Join(repo, "staged-only.txt"), "safe worktree replacement\n")

	findings, err := ScanRepository(context.Background(), repo, false, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, findings, "worktree", "tracked.txt")
	assertFinding(t, findings, "worktree", "untracked.bin")
	assertFinding(t, findings, "index", "staged-only.txt")
	assertRedacted(t, findings, trackedCanary, untrackedCanary, stagedCanary)
}

func TestScanHistoryCoversPublishableRefsAndExcludesPrivateObjects(t *testing.T) {
	repo := newGitRepository(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".local/\n")
	writeFile(t, filepath.Join(repo, "safe.txt"), "safe\n")
	git(t, repo, nil, "add", ".gitignore", "safe.txt")
	git(t, repo, nil, "commit", "-m", "seed")

	historyCanary := secretCanary("hf_")
	writeFile(t, filepath.Join(repo, "history.bin"), historyCanary)
	git(t, repo, nil, "add", "history.bin")
	git(t, repo, nil, "commit", "-m", "history fixture")
	historyOID := strings.TrimSpace(git(t, repo, nil, "rev-parse", "HEAD:history.bin"))
	git(t, repo, nil, "rm", "history.bin")
	git(t, repo, nil, "commit", "-m", "remove fixture")

	danglingCanary := secretCanary("npm_")
	danglingOID := strings.TrimSpace(git(t, repo, []byte(danglingCanary), "hash-object", "-w", "--stdin"))
	privateRefCanary := secretCanary("ghp_")
	writeFile(t, filepath.Join(repo, "private-ref.txt"), privateRefCanary)
	git(t, repo, nil, "add", "private-ref.txt")
	git(t, repo, nil, "commit", "-m", "private tool ref fixture")
	privateRefOID := strings.TrimSpace(git(t, repo, nil, "rev-parse", "HEAD:private-ref.txt"))
	git(t, repo, nil, "update-ref", "refs/codex/snapshots/test", "HEAD")
	git(t, repo, nil, "reset", "--hard", "HEAD^")
	privateMarker := "history-private-marker-" + strings.Repeat("R6", 8)
	markerFile := filepath.Join(repo, ".local", "public-private-markers.txt")
	writeFile(t, markerFile, privateMarker+"\n")
	writeFile(t, filepath.Join(repo, "marker-history.txt"), privateMarker)
	git(t, repo, nil, "add", "marker-history.txt")
	git(t, repo, nil, "commit", "-m", "private marker history fixture")
	markerOID := strings.TrimSpace(git(t, repo, nil, "rev-parse", "HEAD:marker-history.txt"))
	git(t, repo, nil, "rm", "marker-history.txt")
	git(t, repo, nil, "commit", "-m", "remove private marker fixture")
	commitCanary := secretCanary("sk-")
	git(t, repo, nil, "commit", "--allow-empty", "-m", commitCanary)
	commitOID := strings.TrimSpace(git(t, repo, nil, "rev-parse", "HEAD"))
	tagCanary := secretCanary("hf_")
	git(t, repo, nil, "tag", "-a", "secret-scan-fixture", "-m", tagCanary)
	tagOID := strings.TrimSpace(git(t, repo, nil, "rev-parse", "secret-scan-fixture^{tag}"))

	current, err := ScanRepository(context.Background(), repo, false, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Fatalf("current scan finding count = %d, want none", len(current))
	}
	findings, err := ScanRepository(context.Background(), repo, true, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertObjectFinding(t, findings, historyOID)
	assertObjectFinding(t, findings, commitOID)
	assertNoObjectFinding(t, findings, danglingOID)
	assertNoObjectFinding(t, findings, privateRefOID)
	assertNoObjectFinding(t, findings, tagOID)
	for _, finding := range findings {
		if finding.Rule == "private-marker" {
			t.Fatal("default history scan applied local private markers")
		}
	}
	assertRedacted(t, findings, historyCanary, commitCanary, tagCanary, privateRefCanary)
	explicitFindings, err := ScanRepository(context.Background(), repo, true, Options{MarkerFile: markerFile})
	if err != nil {
		t.Fatal(err)
	}
	assertObjectRule(t, explicitFindings, markerOID, "private-marker")
	assertRedacted(t, findings, danglingCanary)
}

func TestScanTreeCoversBinaryZIPAndPrivateMarkers(t *testing.T) {
	root := t.TempDir()
	binaryCanary := secretCanary("glpat-")
	zipCanary := secretCanary("xoxb-")
	zipNameCanary := secretCanary("ghp_")
	markerCanary := "private-marker-" + strings.Repeat("Z9", 8)
	writeFile(t, filepath.Join(root, "raw.bin"), "\x00\x01"+binaryCanary+"\x00")
	writeZIP(t, filepath.Join(root, "bundle.zip"), "nested/secret.bin", []byte("\x00"+zipCanary))
	writeZIP(t, filepath.Join(root, "named.zip"), zipNameCanary+".txt", []byte("safe"))
	markerFile := filepath.Join(t.TempDir(), "markers.txt")
	writeFile(t, markerFile, "# local-only values\n"+markerCanary+"\n")
	writeFile(t, filepath.Join(root, "marker.txt"), markerCanary)

	findings, err := ScanTree(context.Background(), root, Options{MarkerFile: markerFile})
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, findings, "tree", "raw.bin")
	assertFinding(t, findings, "tree", "bundle.zip!nested/secret.bin")
	assertRule(t, findings, "private-marker")
	assertRedacted(t, findings, binaryCanary, zipCanary, zipNameCanary, markerCanary)
}

func TestPrivateMarkerInPathIsDetectedAndRedacted(t *testing.T) {
	root := t.TempDir()
	marker := "local-path-marker-" + strings.Repeat("P4", 8)
	markerFile := filepath.Join(t.TempDir(), "markers.txt")
	writeFile(t, markerFile, marker+"\n")
	writeFile(t, filepath.Join(root, marker+".txt"), "safe")
	findings, err := ScanTree(context.Background(), root, Options{MarkerFile: markerFile})
	if err != nil {
		t.Fatal(err)
	}
	assertRule(t, findings, "private-marker")
	for _, finding := range findings {
		if strings.Contains(finding.Path, marker) {
			t.Fatal("Finding.Path retained a private marker")
		}
	}
	var output bytes.Buffer
	if err := WriteFindings(&output, findings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), marker) {
		t.Fatal("formatter disclosed a private marker from a path")
	}
	if !strings.Contains(output.String(), "<redacted-path:") {
		t.Fatal("formatter did not identify the redacted path")
	}
}

func TestSensitiveFilenamesAndVendorSecretAssignment(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env.local", "client.p12", "id_rsa"} {
		writeFile(t, filepath.Join(root, name), "safe placeholder content")
	}
	assignmentCanary := "A7b9/C2d8+E3f6_G4h5-I0j1=KLMN"
	writeFile(t, filepath.Join(root, "config.txt"), `aws_secret_access_key = "`+assignmentCanary+`"`)
	writeFile(t, filepath.Join(root, "config.yaml"), "service:\n  password: \""+assignmentCanary+"\"\n")
	findings, err := ScanTree(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env.local", "client.p12", "id_rsa"} {
		assertFinding(t, findings, "tree", path)
	}
	assertRule(t, findings, "high-entropy-secret-context")
	assertFinding(t, findings, "tree", "config.yaml")
	assertRedacted(t, findings, assignmentCanary)
}

func TestScanTreeFailsClosedOnMalformedZIPAndLimits(t *testing.T) {
	root := t.TempDir()
	if _, err := ScanTree(context.Background(), root, Options{MarkerFile: filepath.Join(root, "missing-markers.txt")}); err == nil {
		t.Fatal("missing explicit marker file was accepted")
	}
	writeFile(t, filepath.Join(root, "broken.zip"), "not a zip")
	if _, err := ScanTree(context.Background(), root, Options{}); err == nil {
		t.Fatal("malformed ZIP was accepted")
	}
	if err := os.Remove(filepath.Join(root, "broken.zip")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "large.bin"), strings.Repeat("x", 65))
	if _, err := ScanTree(context.Background(), root, Options{MaxBlobBytes: 64}); err == nil {
		t.Fatal("oversized file was accepted")
	}
}

func TestTestPlaceholderAllowlistIsValueBased(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "production.txt"), `token = "test-placeholder-value"`)
	writeFile(t, filepath.Join(root, "fixture_test.go"), secretCanary("sk-"))
	findings, err := ScanTree(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Path != "fixture_test.go" {
		t.Fatalf("finding count = %d, want only non-placeholder test-file canary", len(findings))
	}
}

func TestAssignmentReferencesRequireCompleteSyntax(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "allowed.yaml"), "service:\n  password: ${PASSWORD_REF}\n")
	writeFile(t, filepath.Join(root, "allowed-template.yaml"), "service:\n  password: {{ secret.runtime.password }}\n")
	writeFile(t, filepath.Join(root, "allowed.json"), `{"token":"{{ secret.runtime.token }}"}`)
	writeFile(t, filepath.Join(root, "allowed.env"), "PASSWORD=$PASSWORD_REF\n")
	writeFile(t, filepath.Join(root, "blocked.yaml"), "service:\n  password: %"+strings.Repeat("A7b9", 8)+"\n")
	writeFile(t, filepath.Join(root, "blocked-template.yaml"), "service:\n  password: {{ arbitrary value }}\n")
	writeFile(t, filepath.Join(root, "blocked.json"), `{"to`+`ken":"config.invalid selector"}`)
	writeFile(t, filepath.Join(root, "blocked.env"), "PASSWORD=$BROKEN-SUFFIX\n")
	findings, err := ScanTree(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"blocked.yaml", "blocked-template.yaml", "blocked.json", "blocked.env"} {
		assertFinding(t, findings, "tree", path)
	}
	for _, finding := range findings {
		if strings.HasPrefix(finding.Path, "allowed.") {
			t.Fatalf("complete reference in %s was reported", finding.Path)
		}
	}
}

func TestResourceLimitsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		setup   func(*testing.T, string)
	}{
		{name: "total bytes", options: Options{MaxTotalBytes: 15}, setup: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a.txt"), "1234567890")
			writeFile(t, filepath.Join(root, "b.txt"), "1234567890")
		}},
		{name: "files", options: Options{MaxFiles: 1}, setup: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a.txt"), "safe")
			writeFile(t, filepath.Join(root, "b.txt"), "safe")
		}},
		{name: "findings", options: Options{MaxFindings: 1}, setup: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "tokens.txt"), secretCanary("sk-")+"\n"+secretCanary("ghp_"))
		}},
		{name: "ZIP entries", options: Options{MaxZIPEntries: 1}, setup: func(t *testing.T, root string) {
			writeZIPEntries(t, filepath.Join(root, "many.zip"), map[string][]byte{"a": []byte("safe"), "b": []byte("safe")})
		}},
		{name: "ZIP expanded bytes", options: Options{MaxZIPExpanded: 8}, setup: func(t *testing.T, root string) {
			writeZIP(t, filepath.Join(root, "expanded.zip"), "entry", []byte("more than eight bytes"))
		}},
		{name: "ZIP entry bytes", options: Options{MaxZIPEntryBytes: 8}, setup: func(t *testing.T, root string) {
			writeZIP(t, filepath.Join(root, "entry.zip"), "entry", []byte("more than eight bytes"))
		}},
		{name: "ZIP nesting", options: Options{MaxZIPNestingDepth: 1}, setup: func(t *testing.T, root string) {
			inner := zipBytes(t, map[string][]byte{"payload.txt": []byte("safe")})
			writeZIPEntries(t, filepath.Join(root, "outer.zip"), map[string][]byte{"inner.zip": inner})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			if _, err := ScanTree(context.Background(), root, test.options); err == nil {
				t.Fatal("scan accepted input beyond its configured limit")
			}
		})
	}
}

func TestDenseMatchesFailClosedAtBoundedObservationLimit(t *testing.T) {
	root := t.TempDir()
	const maxFindings = 32
	privateKeyHeader := []byte("-----BEGIN " + "PRIVATE KEY-----\n")
	payload := bytes.Repeat(privateKeyHeader, 200_000)
	writeFile(t, filepath.Join(root, "dense.txt"), string(payload))
	findings, err := ScanTree(context.Background(), root, Options{
		MaxBlobBytes:  int64(len(payload) + 1),
		MaxTotalBytes: int64(len(payload) + 1),
		MaxFindings:   maxFindings,
	})
	if err == nil || !strings.Contains(err.Error(), "finding limit") {
		t.Fatalf("dense scan error = %v, want finding limit", err)
	}
	if len(findings) != 0 {
		t.Fatalf("failed scan returned %d partial findings", len(findings))
	}
}

func BenchmarkDenseSecretScan64MiB(b *testing.B) {
	line := []byte("-----BEGIN " + "PRIVATE KEY-----\n")
	payload := bytes.Repeat(line, (64<<20)/len(line)+1)
	payload = payload[:64<<20]
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		s, err := newScanner(context.Background(), Options{
			MaxBlobBytes:  int64(len(payload)),
			MaxTotalBytes: int64(len(payload)),
			MaxFindings:   10_000,
		})
		if err != nil {
			b.Fatal(err)
		}
		if err := s.scanBytes(location{source: "benchmark", path: "dense.txt"}, payload, 0); err == nil || !strings.Contains(err.Error(), "finding limit") {
			b.Fatalf("dense scan error = %v, want finding limit", err)
		}
	}
}

func TestRepositoryFileLimitFailsClosed(t *testing.T) {
	repo := newGitRepository(t)
	writeFile(t, filepath.Join(repo, "a.txt"), "safe")
	writeFile(t, filepath.Join(repo, "b.txt"), "safe")
	git(t, repo, nil, "add", "a.txt", "b.txt")
	git(t, repo, nil, "commit", "-m", "seed")
	if _, err := ScanRepository(context.Background(), repo, false, Options{MaxFiles: 1}); err == nil {
		t.Fatal("repository scan accepted input beyond its file limit")
	}
}

func TestSelfTestDetectsWithoutDisclosure(t *testing.T) {
	if err := SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, nil, "init", "-q")
	git(t, repo, nil, "config", "user.name", "Secret Scan Test")
	git(t, repo, nil, "config", "user.email", "secretscan@example.invalid")
	return repo
}

func git(t *testing.T, repo string, stdin []byte, args ...string) string {
	t.Helper()
	gitArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader(stdin)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v (%s)", args[0], err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeZIP(t *testing.T, path, name string, content []byte) {
	t.Helper()
	writeZIPEntries(t, path, map[string][]byte{name: content})
}

func writeZIPEntries(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	archive := zipBytes(t, entries)
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
}

func zipBytes(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, content := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func secretCanary(prefix string) string {
	return prefix + strings.Repeat("A7b9", 10)
}

func assertFinding(t *testing.T, findings []Finding, source, path string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Source == source && finding.Path == path {
			return
		}
	}
	t.Fatalf("missing finding source=%s path=%s (finding count %d)", source, path, len(findings))
}

func assertObjectFinding(t *testing.T, findings []Finding, oid string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Source == "history" && finding.ObjectID == oid {
			return
		}
	}
	t.Fatalf("missing history finding for object %s", oid)
}

func assertNoObjectFinding(t *testing.T, findings []Finding, oid string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Source == "history" && finding.ObjectID == oid {
			t.Fatalf("unexpected history finding for private object %s", oid)
		}
	}
}

func assertObjectRule(t *testing.T, findings []Finding, oid, rule string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Source == "history" && finding.ObjectID == oid && finding.Rule == rule {
			return
		}
	}
	t.Fatalf("missing history finding for object %s rule %s", oid, rule)
}

func assertRule(t *testing.T, findings []Finding, rule string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Rule == rule {
			return
		}
	}
	t.Fatalf("missing rule %s (finding count %d)", rule, len(findings))
}

func assertRedacted(t *testing.T, findings []Finding, canaries ...string) {
	t.Helper()
	var output bytes.Buffer
	if err := WriteFindings(&output, findings); err != nil {
		t.Fatal(err)
	}
	for _, canary := range canaries {
		if strings.Contains(output.String(), canary) {
			t.Fatal("formatted finding disclosed a canary")
		}
	}
}
