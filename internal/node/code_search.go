package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
)

const (
	searchRipgrepComponentID = "search-ripgrep"
	maxSearchGlobs           = 32
	maxSearchGlobLength      = 256
	maxSearchContextLines    = 10
	maxRipgrepOutputBytes    = 8 << 20
	maxRipgrepJSONLineBytes  = 1 << 20
	maxSearchResultJSONBytes = 640 << 10
	managedSearchTimeout     = 5 * time.Second
)

var errRipgrepOutputLimit = errors.New("ripgrep output limit exceeded")

type compiledSearchGlob struct {
	raw string
	rx  *regexp.Regexp
}

func (c *Client) codeSearchV2(ctx context.Context, params map[string]any) (codeSearchResult, error) {
	started := time.Now()
	input, searchRoot, includes, excludes, matcher, err := normalizeCodeSearch(params)
	if err != nil {
		return codeSearchResult{}, err
	}

	primaryStarted := time.Now()
	rgCtx, cancelRG := context.WithTimeout(ctx, managedSearchTimeout)
	result, fallbackReason, rgErr := c.searchWithManagedRipgrep(rgCtx, searchRoot, input)
	cancelRG()
	primaryElapsed := time.Since(primaryStarted).Milliseconds()
	if rgErr == nil {
		result.Engine = "ripgrep"
		result.PrimaryElapsedMs = primaryElapsed
		result.ElapsedMs = time.Since(started).Milliseconds()
		return boundCodeSearchResult(result), nil
	}
	if err := ctx.Err(); err != nil {
		return codeSearchResult{}, err
	}
	c.cfg.Logger.Warn("managed ripgrep fallback", "reasonCode", fallbackReason, "elapsedMs", primaryElapsed)
	fallbackStarted := time.Now()
	result, err = searchNative(ctx, searchRoot, input, includes, excludes, matcher)
	if err != nil {
		return codeSearchResult{}, err
	}
	result.Engine = "native"
	result.FallbackReason = fallbackReason
	result.PrimaryElapsedMs = primaryElapsed
	result.FallbackElapsedMs = time.Since(fallbackStarted).Milliseconds()
	result.ElapsedMs = time.Since(started).Milliseconds()
	return boundCodeSearchResult(result), nil
}

func boundCodeSearchResult(result codeSearchResult) codeSearchResult {
	raw, _ := json.Marshal(result)
	if len(raw) <= maxSearchResultJSONBytes {
		return result
	}
	result.Truncated = true
	if len(result.Matches) > 0 {
		original := result.Matches
		low, high := 0, len(original)
		for low < high {
			mid := (low + high + 1) / 2
			candidate := result
			candidate.Matches = original[:mid]
			encoded, _ := json.Marshal(candidate)
			if len(encoded) <= maxSearchResultJSONBytes {
				low = mid
			} else {
				high = mid - 1
			}
		}
		result.Matches = append([]codeSearchMatch(nil), original[:low]...)
	}
	if len(result.Files) > 0 {
		original := result.Files
		low, high := 0, len(original)
		for low < high {
			mid := (low + high + 1) / 2
			candidate := result
			candidate.Files = original[:mid]
			encoded, _ := json.Marshal(candidate)
			if len(encoded) <= maxSearchResultJSONBytes {
				low = mid
			} else {
				high = mid - 1
			}
		}
		result.Files = append([]string(nil), original[:low]...)
	}
	return result
}

func normalizeCodeSearch(params map[string]any) (codeSearchParams, string, []compiledSearchGlob, []compiledSearchGlob, lineMatcher, error) {
	var input codeSearchParams
	if err := decodeParams(params, &input); err != nil {
		return input, "", nil, nil, nil, fmt.Errorf("invalid params: %w", err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || len(input.Query) > 512 || strings.IndexByte(input.Query, 0) >= 0 {
		return input, "", nil, nil, nil, fmt.Errorf("query is required and must be at most 512 characters")
	}
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if input.Mode == "" {
		input.Mode = "content"
	}
	if input.Mode != "content" && input.Mode != "files" {
		return input, "", nil, nil, nil, fmt.Errorf("mode must be content or files")
	}
	if input.Limit == 0 {
		input.Limit = defaultSearchLimit
	}
	if input.Limit < 1 || input.Limit > maxSearchLimit {
		return input, "", nil, nil, nil, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}
	if input.Context < 0 || input.Context > maxSearchContextLines || input.BeforeContext < 0 || input.BeforeContext > maxSearchContextLines || input.AfterContext < 0 || input.AfterContext > maxSearchContextLines {
		return input, "", nil, nil, nil, fmt.Errorf("context values must be between 0 and %d", maxSearchContextLines)
	}
	if input.Context > 0 {
		if input.BeforeContext == 0 {
			input.BeforeContext = input.Context
		}
		if input.AfterContext == 0 {
			input.AfterContext = input.Context
		}
	}
	if strings.TrimSpace(input.Path) == "" {
		return input, "", nil, nil, nil, fmt.Errorf("absolute search path is required")
	}
	searchRoot, err := ResolveMachinePath(input.Path)
	if err != nil {
		return input, "", nil, nil, nil, err
	}
	info, err := os.Stat(searchRoot)
	if err != nil {
		return input, "", nil, nil, nil, err
	}
	if !info.IsDir() {
		return input, "", nil, nil, nil, fmt.Errorf("search path must be a directory")
	}
	matcher, err := compileSearchMatcher(input.Query, input.Regex, input.IgnoreCase)
	if err != nil {
		return input, "", nil, nil, nil, err
	}
	includes, err := compileSearchGlobs(input.Include)
	if err != nil {
		return input, "", nil, nil, nil, fmt.Errorf("include: %w", err)
	}
	excludes, err := compileSearchGlobs(input.Exclude)
	if err != nil {
		return input, "", nil, nil, nil, fmt.Errorf("exclude: %w", err)
	}
	return input, searchRoot, includes, excludes, matcher, nil
}

func compileSearchGlobs(patterns []string) ([]compiledSearchGlob, error) {
	if len(patterns) > maxSearchGlobs {
		return nil, fmt.Errorf("at most %d globs are allowed", maxSearchGlobs)
	}
	result := make([]compiledSearchGlob, 0, len(patterns))
	for _, raw := range patterns {
		if strings.ContainsAny(raw, "\x00\r\n[]") {
			return nil, fmt.Errorf("glob is empty, unsafe, or too long")
		}
		pattern := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if pattern == "" || len(pattern) > maxSearchGlobLength || strings.HasPrefix(pattern, "-") || strings.HasPrefix(pattern, "!") || strings.ContainsAny(pattern, "{}") || strings.HasPrefix(pattern, "/") {
			return nil, fmt.Errorf("glob is empty, unsafe, or too long")
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return nil, fmt.Errorf("glob traversal is not allowed")
			}
		}
		rx, err := regexp.Compile(searchGlobRegexp(pattern))
		if err != nil {
			return nil, fmt.Errorf("invalid glob")
		}
		result = append(result, compiledSearchGlob{raw: pattern, rx: rx})
	}
	return result, nil
}

func searchGlobRegexp(pattern string) string {
	var out strings.Builder
	out.WriteByte('^')
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
			} else {
				i++
				out.WriteString("[^/]*")
			}
		case '?':
			i++
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			i++
		}
	}
	out.WriteByte('$')
	return out.String()
}

func matchesSearchGlobs(relativePath string, includes, excludes []compiledSearchGlob) bool {
	relativePath = filepath.ToSlash(relativePath)
	if len(includes) > 0 {
		matched := false
		for _, include := range includes {
			if include.rx.MatchString(relativePath) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, exclude := range excludes {
		if exclude.rx.MatchString(relativePath) {
			return false
		}
	}
	return true
}

func searchNative(ctx context.Context, searchRoot string, input codeSearchParams, includes, excludes []compiledSearchGlob, matcher lineMatcher) (codeSearchResult, error) {
	result := codeSearchResult{Matches: []codeSearchMatch{}, Files: []string{}}
	matchedFiles := map[string]bool{}
	candidates, ignoredByVCS, candidateTruncated, candidateSkipped, candidateSkipReasons, candidateIncomplete, err := nativeSearchCandidates(ctx, searchRoot, includes, excludes)
	if err != nil {
		return codeSearchResult{}, err
	}
	result.Truncated = candidateTruncated
	result.SkippedFiles = candidateSkipped
	result.SkipReasons = candidateSkipReasons
	result.Incomplete = candidateIncomplete
	if !ignoredByVCS {
		if _, statErr := os.Stat(filepath.Join(searchRoot, ".gitignore")); statErr == nil {
			markSearchSkip(&result, "vcs_ignore_unavailable", true)
		}
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return codeSearchResult{}, err
		}
		if !matchesSearchGlobs(candidate.relative, includes, excludes) {
			continue
		}
		if candidate.size > maxSearchFileBytes {
			markSearchSkip(&result, "too_large", false)
			continue
		}
		result.ScannedFiles++
		matches, totalMatches, binary, bytesScanned, err := searchNativeFile(candidate.path, candidate.relative, matcher, input.BeforeContext, input.AfterContext, input.Limit)
		if err != nil {
			markSearchSkip(&result, "read_error", true)
			continue
		}
		result.BytesScanned += bytesScanned
		if binary {
			markSearchSkip(&result, "not_text", false)
			continue
		}
		if totalMatches == 0 {
			continue
		}
		result.MatchCount += totalMatches
		matchedFiles[candidate.relative] = true
		if input.Mode == "files" {
			if len(result.Files) < input.Limit {
				result.Files = append(result.Files, candidate.relative)
			} else {
				result.Truncated = true
			}
			continue
		}
		remaining := input.Limit - len(result.Matches)
		if remaining <= 0 {
			result.Truncated = true
			continue
		}
		if len(matches) > remaining {
			matches = matches[:remaining]
			result.Truncated = true
		}
		result.Matches = append(result.Matches, matches...)
		if totalMatches > len(matches) {
			result.Truncated = true
		}
	}
	result.MatchedFiles = len(matchedFiles)
	return result, nil
}

type nativeSearchCandidate struct {
	path     string
	relative string
	size     int64
}

func nativeSearchCandidates(ctx context.Context, searchRoot string, includes, excludes []compiledSearchGlob) ([]nativeSearchCandidate, bool, bool, int, map[string]int, bool, error) {
	explicitIncludes := len(includes) > 0
	if !explicitIncludes {
		if candidates, ok := gitSearchCandidates(ctx, searchRoot); ok {
			filtered := candidates[:0]
			for _, candidate := range candidates {
				if matchesSearchGlobs(candidate.relative, includes, excludes) {
					filtered = append(filtered, candidate)
				}
			}
			candidates = filtered
			truncated := len(candidates) > maxSearchFiles
			if truncated {
				candidates = candidates[:maxSearchFiles]
			}
			return candidates, true, truncated, 0, nil, false, nil
		}
	}
	candidates := make([]nativeSearchCandidate, 0)
	truncated := false
	skipped := 0
	skipReasons := map[string]int{}
	incomplete := false
	markCandidateSkip := func(reason string) {
		skipped++
		skipReasons[reason]++
		incomplete = true
	}
	err := filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			markCandidateSkip("walk_error")
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != searchRoot && (entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") || (!explicitIncludes && shouldSkipDir(entry.Name()))) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			markCandidateSkip("path_error")
			return nil
		}
		if shouldSkipSearchRelative(rel, !explicitIncludes) {
			return nil
		}
		if !matchesSearchGlobs(filepath.ToSlash(rel), includes, excludes) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			markCandidateSkip("stat_error")
			return nil
		}
		if len(candidates) >= maxSearchFiles {
			truncated = true
			return filepath.SkipAll
		}
		candidates = append(candidates, nativeSearchCandidate{path: path, relative: filepath.ToSlash(rel), size: info.Size()})
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, false, false, skipped, skipReasons, true, err
	}
	if len(skipReasons) == 0 {
		skipReasons = nil
	}
	return candidates, false, truncated, skipped, skipReasons, incomplete, nil
}

func gitSearchCandidates(ctx context.Context, searchRoot string) ([]nativeSearchCandidate, bool) {
	stdout := &boundedCommandBuffer{limit: 16 << 20}
	command := exec.CommandContext(ctx, "git", "-c", "core.quotepath=false", "-C", searchRoot, "ls-files", "-co", "--exclude-standard", "-z", "--", ".")
	command.Env = safeShellEnvironment()
	command.Stdout = stdout
	command.Stderr = &boundedCommandBuffer{limit: 16 << 10}
	if err := command.Run(); err != nil || stdout.err != nil {
		return nil, false
	}
	parts := bytes.Split(stdout.Bytes(), []byte{0})
	candidates := make([]nativeSearchCandidate, 0, len(parts))
	for _, raw := range parts {
		if len(raw) == 0 || !utf8.Valid(raw) {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(string(raw)))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || shouldSkipSearchRelative(rel, true) {
			continue
		}
		path := filepath.Join(searchRoot, rel)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidates = append(candidates, nativeSearchCandidate{path: path, relative: filepath.ToSlash(rel), size: info.Size()})
	}
	return candidates, true
}

func shouldSkipSearchRelative(relative string, skipGenerated bool) bool {
	parts := strings.FieldsFunc(filepath.ToSlash(relative), func(r rune) bool { return r == '/' })
	for _, part := range parts {
		if strings.HasPrefix(part, ".") || (skipGenerated && shouldSkipDir(part)) {
			return true
		}
	}
	return false
}

func markSearchSkip(result *codeSearchResult, reason string, incomplete bool) {
	result.SkippedFiles++
	if result.SkipReasons == nil {
		result.SkipReasons = map[string]int{}
	}
	result.SkipReasons[reason]++
	result.Incomplete = result.Incomplete || incomplete
}

func searchNativeFile(path, relativePath string, matcher lineMatcher, beforeCount, afterCount, maxMatches int) ([]codeSearchMatch, int, bool, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, 0, err
	}
	if len(raw) > maxSearchFileBytes || bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return nil, 0, true, int64(len(raw)), nil
	}
	lines := strings.Split(string(raw), "\n")
	matches := make([]codeSearchMatch, 0)
	totalMatches := 0
	for index, value := range lines {
		line := strings.TrimSuffix(value, "\r")
		column, ok := matcher(line)
		if !ok {
			continue
		}
		totalMatches++
		if len(matches) >= maxMatches {
			continue
		}
		match := codeSearchMatch{Path: relativePath, Line: index + 1, Column: column, Text: truncateSearchText(line)}
		for contextIndex := maxInt(0, index-beforeCount); contextIndex < index; contextIndex++ {
			match.Before = append(match.Before, codeSearchContextLine{Line: contextIndex + 1, Text: truncateSearchText(strings.TrimSuffix(lines[contextIndex], "\r"))})
		}
		for contextIndex := index + 1; contextIndex < len(lines) && contextIndex <= index+afterCount; contextIndex++ {
			match.After = append(match.After, codeSearchContextLine{Line: contextIndex + 1, Text: truncateSearchText(strings.TrimSuffix(lines[contextIndex], "\r"))})
		}
		matches = append(matches, match)
	}
	return matches, totalMatches, false, int64(len(raw)), nil
}

func truncateSearchText(value string) string {
	if len(value) <= maxSearchLineLength {
		return value
	}
	value = value[:maxSearchLineLength]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (c *Client) searchWithManagedRipgrep(ctx context.Context, searchRoot string, input codeSearchParams) (codeSearchResult, string, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return codeSearchResult{}, "RG_NOT_FOUND", errors.New("ripgrep platform unsupported")
	}
	executableName := "rg"
	if runtime.GOOS == "windows" {
		executableName = "rg.exe"
	}
	_, executablePath, err := componentmgr.FindInstalledExecutable(c.cfg.DataDir, searchRipgrepComponentID, executableName)
	if err != nil {
		return codeSearchResult{}, "RG_NOT_FOUND", err
	}
	args := buildRipgrepArgs(input, searchRoot)
	command := exec.CommandContext(ctx, executablePath, args...)
	command.Dir = searchRoot
	command.Env = ripgrepEnvironment(os.Environ())
	stdout := &boundedCommandBuffer{limit: maxRipgrepOutputBytes}
	stderr := &boundedCommandBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if errors.Is(stdout.err, errRipgrepOutputLimit) {
		return codeSearchResult{}, "RG_OUTPUT_LIMIT", errRipgrepOutputLimit
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return codeSearchResult{}, "RG_TIMEOUT", ctx.Err()
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return codeSearchResult{}, "RG_START_FAILED", err
		}
		if exitErr.ExitCode() != 1 {
			return codeSearchResult{}, "RG_EXIT_ERROR", errors.New("managed ripgrep command failed")
		}
	}
	result, err := parseRipgrepJSON(stdout.Bytes(), searchRoot, input)
	if err != nil {
		return codeSearchResult{}, "RG_OUTPUT_INVALID", err
	}
	return result, "", nil
}

func buildRipgrepArgs(input codeSearchParams, searchRoot string) []string {
	args := []string{"--json", "--no-config", "--color=never", "--no-heading", "--line-number", "--column", "--max-filesize", strconv.Itoa(maxSearchFileBytes)}
	if input.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if !input.Regex {
		args = append(args, "--fixed-strings")
	}
	if input.BeforeContext > 0 {
		args = append(args, "--before-context", strconv.Itoa(input.BeforeContext))
	}
	if input.AfterContext > 0 {
		args = append(args, "--after-context", strconv.Itoa(input.AfterContext))
	}
	for _, pattern := range input.Include {
		args = append(args, "--glob", strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"))
	}
	for _, pattern := range input.Exclude {
		args = append(args, "--glob", "!"+strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"))
	}
	if len(input.Include) > 0 {
		// An explicit include is authoritative, including for VCS-ignored generated
		// paths. Its bounded glob still constrains what ripgrep can visit.
		args = append(args, "--no-ignore")
	} else {
		// Ignore only VCS rules. Dot-ignore files (.ignore/.rgignore) are not
		// applied so the managed and native engines have the same contract.
		args = append(args, "--no-ignore-dot")
		for _, directory := range []string{"node_modules", "vendor", ".idea", ".vscode", ".next", ".nuxt", ".cache", "coverage", "dist", "build", "out", "target", "bin", "obj"} {
			args = append(args, "--glob", "!"+directory+"/**", "--glob", "!**/"+directory+"/**")
		}
	}
	args = append(args, "--regexp", input.Query, "--", searchRoot)
	return args
}

func ripgrepEnvironment(input []string) []string {
	result := make([]string, 0, len(input)+1)
	for _, item := range input {
		if strings.EqualFold(strings.SplitN(item, "=", 2)[0], "RIPGREP_CONFIG_PATH") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "RIPGREP_CONFIG_PATH=")
}

type boundedCommandBuffer struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func (b *boundedCommandBuffer) Write(value []byte) (int, error) {
	if b.buffer.Len()+len(value) > b.limit {
		remaining := b.limit - b.buffer.Len()
		if remaining > 0 {
			_, _ = b.buffer.Write(value[:remaining])
		}
		b.err = errRipgrepOutputLimit
		return 0, b.err
	}
	return b.buffer.Write(value)
}

func (b *boundedCommandBuffer) Bytes() []byte { return b.buffer.Bytes() }

type ripgrepJSONEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
		Stats struct {
			Searches          int   `json:"searches"`
			SearchesWithMatch int   `json:"searches_with_match"`
			BytesSearched     int64 `json:"bytes_searched"`
			Matches           int   `json:"matches"`
			MatchedLines      int   `json:"matched_lines"`
		} `json:"stats"`
	} `json:"data"`
}

func parseRipgrepJSON(raw []byte, searchRoot string, input codeSearchParams) (codeSearchResult, error) {
	result := codeSearchResult{Matches: []codeSearchMatch{}, Files: []string{}}
	pending := map[string][]codeSearchContextLine{}
	lastMatch := map[string]int{}
	seenFiles, scanned := map[string]bool{}, map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), maxRipgrepJSONLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event ripgrepJSONEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return codeSearchResult{}, errors.New("invalid ripgrep JSON event")
		}
		if event.Type != "begin" && event.Type != "match" && event.Type != "context" && event.Type != "end" && event.Type != "summary" {
			return codeSearchResult{}, errors.New("unknown ripgrep JSON event")
		}
		if event.Type == "summary" {
			result.ScannedFiles = event.Data.Stats.Searches
			result.MatchedFiles = event.Data.Stats.SearchesWithMatch
			result.BytesScanned = event.Data.Stats.BytesSearched
			// matchCount is the number of matching lines, consistently across
			// managed ripgrep and the native fallback. Multiple occurrences on one
			// line still produce one returned match record.
			result.MatchCount = event.Data.Stats.MatchedLines
			continue
		}
		path, err := safeRipgrepRelativePath(searchRoot, event.Data.Path.Text)
		if err != nil {
			return codeSearchResult{}, err
		}
		if event.Type == "begin" {
			scanned[path] = true
			continue
		}
		if event.Type == "end" {
			delete(lastMatch, path)
			delete(pending, path)
			continue
		}
		text := truncateSearchText(strings.TrimSuffix(strings.TrimSuffix(event.Data.Lines.Text, "\n"), "\r"))
		if event.Type == "context" {
			contextLine := codeSearchContextLine{Line: event.Data.LineNumber, Text: text}
			if index, ok := lastMatch[path]; ok && index < len(result.Matches) && event.Data.LineNumber > result.Matches[index].Line {
				result.Matches[index].After = append(result.Matches[index].After, contextLine)
			} else {
				pending[path] = append(pending[path], contextLine)
			}
			continue
		}
		if event.Data.LineNumber < 1 || len(event.Data.Submatches) == 0 || event.Data.Submatches[0].Start < 0 {
			return codeSearchResult{}, errors.New("incomplete ripgrep match event")
		}
		if input.Mode == "files" {
			if !seenFiles[path] {
				seenFiles[path] = true
				if len(result.Files) < input.Limit {
					result.Files = append(result.Files, path)
				} else {
					result.Truncated = true
				}
			}
			continue
		}
		if len(result.Matches) >= input.Limit {
			result.Truncated = true
			continue
		}
		match := codeSearchMatch{Path: path, Line: event.Data.LineNumber, Column: event.Data.Submatches[0].Start + 1, Text: text}
		match.Before = append(match.Before, pending[path]...)
		delete(pending, path)
		result.Matches = append(result.Matches, match)
		lastMatch[path] = len(result.Matches) - 1
	}
	if err := scanner.Err(); err != nil {
		return codeSearchResult{}, errors.New("ripgrep JSON line exceeds limit")
	}
	if result.ScannedFiles == 0 {
		result.ScannedFiles = len(scanned)
	}
	if result.MatchedFiles == 0 {
		result.MatchedFiles = len(seenFiles)
		if input.Mode != "files" {
			matched := map[string]bool{}
			for _, item := range result.Matches {
				matched[item.Path] = true
			}
			result.MatchedFiles = len(matched)
		}
	}
	if result.MatchCount == 0 {
		if input.Mode == "files" {
			result.MatchCount = len(result.Files)
		} else {
			result.MatchCount = len(result.Matches)
		}
	}
	return result, nil
}

func safeRipgrepRelativePath(searchRoot, rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" || strings.IndexByte(rawPath, 0) >= 0 {
		return "", errors.New("ripgrep result path is missing")
	}
	path := filepath.FromSlash(rawPath)
	if !filepath.IsAbs(path) {
		path = filepath.Join(searchRoot, path)
	}
	relative, err := filepath.Rel(searchRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("ripgrep result path escapes search root")
	}
	return filepath.ToSlash(relative), nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
