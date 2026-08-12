package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

const fakeRipgrepScenarioEnv = "FAST_SPIDER_FAKE_RG_SCENARIO"

func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeRipgrepScenarioEnv); scenario != "" {
		runFakeRipgrep(scenario)
		return
	}
	os.Exit(m.Run())
}

func runFakeRipgrep(scenario string) {
	capture := struct {
		Executable       string   `json:"executable"`
		Args             []string `json:"args"`
		ConfigPath       string   `json:"configPath"`
		ConfigPathExists bool     `json:"configPathExists"`
	}{Executable: os.Args[0], Args: os.Args[1:]}
	capture.ConfigPath, capture.ConfigPathExists = os.LookupEnv("RIPGREP_CONFIG_PATH")
	if path := os.Getenv("FAST_SPIDER_FAKE_RG_CAPTURE"); path != "" {
		raw, _ := json.Marshal(capture)
		_ = os.WriteFile(path, raw, 0o600)
	}
	if scenario == "invalid" {
		fmt.Println("not-json")
		return
	}
	root := os.Args[len(os.Args)-1]
	emit := func(eventType, path, text string, lineNumber, start int) {
		data := map[string]any{"path": map[string]any{"text": filepath.Join(root, filepath.FromSlash(path))}}
		if eventType == "match" || eventType == "context" {
			data["lines"] = map[string]any{"text": text + "\n"}
			data["line_number"] = lineNumber
			if eventType == "match" {
				data["submatches"] = []map[string]any{{"start": start, "end": start + 6}}
			} else {
				data["submatches"] = []any{}
			}
		}
		raw, _ := json.Marshal(map[string]any{"type": eventType, "data": data})
		fmt.Println(string(raw))
	}
	for _, fixture := range []struct {
		path, before, match, after string
	}{
		{"src/main.go", "before one", "found needle here", "after one"},
		{"src/second.go", "before two", "another needle", "after two"},
	} {
		emit("begin", fixture.path, "", 0, 0)
		emit("context", fixture.path, fixture.before, 1, 0)
		emit("match", fixture.path, fixture.match, 2, strings.Index(fixture.match, "needle"))
		emit("context", fixture.path, fixture.after, 3, 0)
		emit("end", fixture.path, "", 0, 0)
	}
	fmt.Println(`{"type":"summary","data":{"stats":{"searches":77,"searches_with_match":2,"bytes_searched":4096,"matches":2,"matched_lines":2}}}`)
}

func TestCodeSearchResultBudgetPreservesStatsAndFitsHubTransport(t *testing.T) {
	line := strings.Repeat("界", maxSearchLineLength/3)
	result := codeSearchResult{Matches: make([]codeSearchMatch, 200), MatchCount: 900, MatchedFiles: 200, ScannedFiles: 500, Engine: "native"}
	for index := range result.Matches {
		contextLines := make([]codeSearchContextLine, 10)
		for lineIndex := range contextLines {
			contextLines[lineIndex] = codeSearchContextLine{Line: lineIndex + 1, Text: line}
		}
		result.Matches[index] = codeSearchMatch{Path: fmt.Sprintf("src/long-%03d.go", index), Line: 11, Column: 1, Text: line, Before: contextLines, After: contextLines}
	}
	bounded := boundCodeSearchResult(result)
	raw, err := json.Marshal(bounded)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSearchResultJSONBytes || !bounded.Truncated || len(bounded.Matches) >= len(result.Matches) {
		t.Fatalf("bytes=%d truncated=%v matches=%d", len(raw), bounded.Truncated, len(bounded.Matches))
	}
	if bounded.MatchCount != result.MatchCount || bounded.MatchedFiles != result.MatchedFiles || bounded.ScannedFiles != result.ScannedFiles {
		t.Fatalf("budgeting changed aggregate stats: %#v", bounded)
	}
}

func TestNativeSearchCountsBeyondReturnedLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "matches.txt"), []byte("needle\nneedle\nneedle\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := codeSearchParams{Query: "needle", Path: root, Mode: "content", Limit: 2}
	_, searchRoot, includes, excludes, matcher, err := normalizeCodeSearch(map[string]any{"query": input.Query, "path": input.Path, "mode": input.Mode, "limit": input.Limit})
	if err != nil {
		t.Fatal(err)
	}
	result, err := searchNative(context.Background(), searchRoot, input, includes, excludes, matcher)
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchCount != 4 || len(result.Matches) != 2 || !result.Truncated || result.MatchedFiles != 1 {
		t.Fatalf("native result=%#v", result)
	}
}

func TestRipgrepAndNativeMatchCountMeansMatchingLines(t *testing.T) {
	root := t.TempDir()
	matchEvent := fmt.Sprintf(`{"type":"match","data":{"path":{"text":%q},"lines":{"text":"needle needle\n"},"line_number":1,"submatches":[{"start":0},{"start":7}]}}`, filepath.Join(root, "same-line.txt"))
	summary := `{"type":"summary","data":{"stats":{"searches":1,"searches_with_match":1,"bytes_searched":14,"matches":2,"matched_lines":1}}}`
	managed, err := parseRipgrepJSON([]byte(matchEvent+"\n"+summary+"\n"), root, codeSearchParams{Mode: "content", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "same-line.txt"), []byte("needle needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, searchRoot, includes, excludes, matcher, err := normalizeCodeSearch(map[string]any{"query": "needle", "path": root, "mode": "content", "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	native, err := searchNative(context.Background(), searchRoot, codeSearchParams{Query: "needle", Path: root, Mode: "content", Limit: 10}, includes, excludes, matcher)
	if err != nil {
		t.Fatal(err)
	}
	if managed.MatchCount != 1 || native.MatchCount != 1 {
		t.Fatalf("matching-line count managed=%d native=%d", managed.MatchCount, native.MatchCount)
	}
}

func TestNativeExplicitIncludeFiltersBeforeCandidateLimit(t *testing.T) {
	root := t.TempDir()
	noiseDir := filepath.Join(root, "aaa-noise")
	targetDir := filepath.Join(root, "zzz-target")
	if err := os.MkdirAll(noiseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxSearchFiles; index++ {
		path := filepath.Join(noiseDir, fmt.Sprintf("%04d.txt", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(targetDir, "wanted.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	includes, err := compileSearchGlobs([]string{"zzz-target/**"})
	if err != nil {
		t.Fatal(err)
	}
	candidates, _, truncated, _, _, _, err := nativeSearchCandidates(context.Background(), root, includes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(candidates) != 1 || candidates[0].relative != "zzz-target/wanted.txt" {
		t.Fatalf("candidates=%v truncated=%v", candidates, truncated)
	}
}

func TestManagedRipgrepIgnoresOnlyVCSRulesByDefault(t *testing.T) {
	args := buildRipgrepArgs(codeSearchParams{Query: "needle"}, t.TempDir())
	if !stringSliceContains(args, "--no-ignore-dot") {
		t.Fatalf("ripgrep args do not disable .ignore/.rgignore rules: %v", args)
	}
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestManagedRipgrepUsesExactSafeExecutableArgumentsAndEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	executablePath := installFakeManagedRipgrep(t, dataDir)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv(fakeRipgrepScenarioEnv, "valid")
	t.Setenv("FAST_SPIDER_FAKE_RG_CAPTURE", capturePath)
	t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(t.TempDir(), "unsafe.conf"))
	t.Setenv("PATH", t.TempDir())
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.codeSearch(context.Background(), map[string]any{
		"path": root, "query": "needle", "include": []string{"**/*.go"}, "exclude": []string{"**/generated/**"},
		"beforeContext": 1, "afterContext": 1, "limit": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "ripgrep" || result.FallbackReason != "" || len(result.Matches) != 1 || !result.Truncated || result.ScannedFiles != 77 || result.MatchedFiles != 2 || result.BytesScanned != 4096 || result.MatchCount != 2 {
		t.Fatalf("managed search result=%+v", result)
	}
	match := result.Matches[0]
	if match.Path != "src/main.go" || match.Line != 2 || match.Column != 7 || len(match.Before) != 1 || len(match.After) != 1 {
		t.Fatalf("managed match=%+v", match)
	}
	var capture struct {
		Executable       string   `json:"executable"`
		Args             []string `json:"args"`
		ConfigPath       string   `json:"configPath"`
		ConfigPathExists bool     `json:"configPathExists"`
	}
	raw, err := os.ReadFile(capturePath)
	if err != nil || json.Unmarshal(raw, &capture) != nil {
		t.Fatalf("read fake rg capture: %v %s", err, raw)
	}
	if !sameSearchPath(capture.Executable, executablePath) {
		t.Fatalf("executed %q, want managed %q", capture.Executable, executablePath)
	}
	if !capture.ConfigPathExists || capture.ConfigPath != "" {
		t.Fatalf("RIPGREP_CONFIG_PATH was not cleared: %+v", capture)
	}
	for _, required := range []string{"--json", "--no-config", "--color=never", "--no-heading", "--line-number", "--column"} {
		if !containsSearchArg(capture.Args, required) {
			t.Fatalf("managed rg argv missing %q: %v", required, capture.Args)
		}
	}
	for _, forbidden := range []string{"--pre", "--search-zip", "--follow", "--unrestricted"} {
		if containsSearchArg(capture.Args, forbidden) {
			t.Fatalf("managed rg argv contains forbidden %q: %v", forbidden, capture.Args)
		}
	}
	if !containsSearchArgPair(capture.Args, "--glob", "**/*.go") || !containsSearchArgPair(capture.Args, "--glob", "!**/generated/**") {
		t.Fatalf("managed rg argv omitted safe globs: %v", capture.Args)
	}
	if !containsSearchArg(capture.Args, "--no-ignore") {
		t.Fatalf("explicit include did not override VCS ignore: %v", capture.Args)
	}
}

func TestManagedRipgrepFilesModeAndInvalidOutputFallback(t *testing.T) {
	dataDir := t.TempDir()
	installFakeManagedRipgrep(t, dataDir)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("before\nneedle native\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeRipgrepScenarioEnv, "valid")
	files, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle", "mode": "files", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if files.Engine != "ripgrep" || len(files.Files) != 1 || files.Files[0] != "src/main.go" || !files.Truncated || len(files.Matches) != 0 {
		t.Fatalf("ripgrep files result=%+v", files)
	}

	t.Setenv(fakeRipgrepScenarioEnv, "invalid")
	fallback, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle", "context": 1, "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Engine != "native" || fallback.FallbackReason != "RG_OUTPUT_INVALID" || len(fallback.Matches) != 1 || len(fallback.Matches[0].Before) != 1 || len(fallback.Matches[0].After) != 1 {
		t.Fatalf("invalid rg fallback=%+v", fallback)
	}
}

func TestNativeSearchContentFilesGlobsContextAndBounds(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	files := map[string]string{
		"main.go":              "before\nNeedle main\nafter\n",
		"nested/other.go":      "Needle nested\n",
		"nested/excluded.go":   "Needle excluded\n",
		"nested/irrelevant.md": "Needle markdown\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{
		"path": root, "query": "needle", "ignoreCase": true, "include": []string{"**/*.go"},
		"exclude": []string{"**/excluded.go"}, "context": 1, "limit": 10,
	}
	content, err := client.codeSearch(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if content.Engine != "native" || content.FallbackReason != "RG_NOT_FOUND" || len(content.Matches) != 2 || content.Matches[0].Path != "main.go" || len(content.Matches[0].Before) != 1 || len(content.Matches[0].After) != 1 {
		t.Fatalf("native content result=%+v", content)
	}
	params["mode"], params["limit"] = "files", 1
	fileResult, err := client.codeSearch(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if fileResult.Engine != "native" || len(fileResult.Files) != 1 || fileResult.Files[0] != "main.go" || !fileResult.Truncated || len(fileResult.Matches) != 0 {
		t.Fatalf("native files result=%+v", fileResult)
	}
	for _, invalid := range []map[string]any{
		{"path": root, "query": "needle", "mode": "raw"},
		{"path": root, "query": "needle", "include": []string{"--pre"}},
		{"path": root, "query": "needle", "exclude": []string{"../outside/**"}},
		{"path": root, "query": "needle", "include": []string{"**/*.go\n"}},
		{"path": root, "query": "needle", "context": maxSearchContextLines + 1},
		{"path": root, "query": "needle", "rawArgs": []string{"--pre"}},
	} {
		if _, err := client.codeSearch(context.Background(), invalid); err == nil {
			t.Fatalf("unsafe search params accepted: %+v", invalid)
		}
	}
}

func TestManagedRipgrepStartFailureFallsBackWithoutRawError(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "rg"
	if runtime.GOOS == "windows" {
		name = "rg.exe"
	}
	dir := filepath.Join(dataDir, "components", searchRipgrepComponentID, "broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not an executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := releaseinfo.NewManifest("component", searchRipgrepComponentID, runtime.GOOS+"-"+runtime.GOARCH, "broken", strings.Repeat("a", 64), 1, "/private?token=secret")
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, ".fast-spider-component.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "native" || result.FallbackReason != "RG_START_FAILED" || len(result.Matches) != 1 || strings.Contains(result.FallbackReason, "secret") {
		t.Fatalf("start failure fallback=%+v", result)
	}
}

func TestRipgrepJSONParserRejectsOversizedAndEscapingOutput(t *testing.T) {
	root := t.TempDir()
	input := codeSearchParams{Mode: "content", Limit: 10}
	if _, err := parseRipgrepJSON(bytes.Repeat([]byte("x"), maxRipgrepJSONLineBytes+1), root, input); err == nil {
		t.Fatal("oversized ripgrep JSON line was accepted")
	}
	escaping := fmt.Sprintf(`{"type":"match","data":{"path":{"text":%q},"lines":{"text":"needle\n"},"line_number":1,"submatches":[{"start":0,"end":6}]}}`, filepath.Join(root, "..", "outside.go"))
	if _, err := parseRipgrepJSON([]byte(escaping+"\n"), root, input); err == nil {
		t.Fatal("escaping ripgrep result path was accepted")
	}
}

func installFakeManagedRipgrep(t *testing.T, dataDir string) string {
	t.Helper()
	name := "rg"
	if runtime.GOOS == "windows" {
		name = "rg.exe"
	}
	dir := filepath.Join(dataDir, "components", searchRipgrepComponentID, "1.0.0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	executablePath := filepath.Join(dir, name)
	destination, err := os.OpenFile(executablePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := releaseinfo.NewManifest("component", searchRipgrepComponentID, runtime.GOOS+"-"+runtime.GOARCH, "1.0.0", strings.Repeat("a", 64), 1, "/unused")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".fast-spider-component.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return executablePath
}

func sameSearchPath(left, right string) bool {
	left, _ = filepath.Abs(left)
	right, _ = filepath.Abs(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func TestCodeSearchRejectsAmbiguousGlobBeforeExecution(t *testing.T) {
	root := t.TempDir()
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle", "include": []string{"["}}); err == nil || !strings.Contains(err.Error(), "glob") {
		t.Fatalf("ambiguous glob error=%v", err)
	}
}

func TestNativeSearchRespectsGitIgnoreAndGeneratedDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for VCS ignore parity")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	fixtures := map[string]string{
		".gitignore": "ignored.txt\nnested/ignored.go\n",
		"visible.go": "needle visible\n", "ignored.txt": "needle ignored\n",
		"nested/ignored.go": "needle nested ignored\n", "dist/generated.go": "needle generated\n",
	}
	for name, content := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle", "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "native" || result.Incomplete || len(result.Matches) != 1 || result.Matches[0].Path != "visible.go" {
		t.Fatalf("ignore parity result=%+v", result)
	}
	explicit, err := client.codeSearch(context.Background(), map[string]any{"path": root, "query": "needle", "include": []string{"dist/**"}, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Engine != "native" || len(explicit.Matches) != 1 || explicit.Matches[0].Path != "dist/generated.go" || explicit.BytesScanned == 0 || explicit.MatchCount != 1 {
		t.Fatalf("explicit generated-dir include result=%+v", explicit)
	}
}

func containsSearchArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsSearchArgPair(args []string, key, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return true
		}
	}
	return false
}
