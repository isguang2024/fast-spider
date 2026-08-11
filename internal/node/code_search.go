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

	result, fallbackReason, rgErr := c.searchWithManagedRipgrep(ctx, searchRoot, input)
	if rgErr == nil {
		result.Engine = "ripgrep"
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return codeSearchResult{}, err
	}
	result, err = searchNative(ctx, searchRoot, input, includes, excludes, matcher)
	if err != nil {
		return codeSearchResult{}, err
	}
	result.Engine = "native"
	result.FallbackReason = fallbackReason
	result.ElapsedMs = time.Since(started).Milliseconds()
	return result, nil
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
		if strings.ContainsAny(raw, "\x00\r\n") {
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
	err := filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != searchRoot && (entry.Type()&os.ModeSymlink != 0 || shouldSkipDir(entry.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil || !matchesSearchGlobs(rel, includes, excludes) {
			return nil
		}
		if result.ScannedFiles >= maxSearchFiles {
			result.Truncated = true
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxSearchFileBytes {
			return nil
		}
		result.ScannedFiles++
		matches, binary, err := searchNativeFile(path, filepath.ToSlash(rel), matcher, input.BeforeContext, input.AfterContext, input.Limit)
		if err != nil || binary || len(matches) == 0 {
			return nil
		}
		if input.Mode == "files" {
			result.Files = append(result.Files, filepath.ToSlash(rel))
			if len(result.Files) >= input.Limit {
				result.Truncated = true
				return filepath.SkipAll
			}
			return nil
		}
		remaining := input.Limit - len(result.Matches)
		if len(matches) > remaining {
			matches = matches[:remaining]
			result.Truncated = true
		}
		result.Matches = append(result.Matches, matches...)
		if len(result.Matches) >= input.Limit {
			result.Truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return codeSearchResult{}, err
	}
	return result, nil
}

func searchNativeFile(path, relativePath string, matcher lineMatcher, beforeCount, afterCount, maxMatches int) ([]codeSearchMatch, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxSearchFileBytes || bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return nil, true, nil
	}
	lines := strings.Split(string(raw), "\n")
	matches := make([]codeSearchMatch, 0)
	for index, value := range lines {
		line := strings.TrimSuffix(value, "\r")
		column, ok := matcher(line)
		if !ok {
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
		if len(matches) >= maxMatches {
			break
		}
	}
	return matches, false, nil
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
		return codeSearchResult{}, "platform_unsupported", errors.New("ripgrep platform unsupported")
	}
	executableName := "rg"
	if runtime.GOOS == "windows" {
		executableName = "rg.exe"
	}
	_, executablePath, err := componentmgr.FindInstalledExecutable(c.cfg.DataDir, searchRipgrepComponentID, executableName)
	if err != nil {
		reason := "component_invalid"
		if errors.Is(err, componentmgr.ErrComponentNotInstalled) {
			reason = "component_missing"
		}
		return codeSearchResult{}, reason, err
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
		return codeSearchResult{}, "output_limit", errRipgrepOutputLimit
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return codeSearchResult{}, "start_failed", err
		}
		if exitErr.ExitCode() != 1 {
			return codeSearchResult{}, "command_failed", errors.New("managed ripgrep command failed")
		}
	}
	result, err := parseRipgrepJSON(stdout.Bytes(), searchRoot, input)
	if err != nil {
		return codeSearchResult{}, "output_invalid", err
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
	result.ScannedFiles = len(scanned)
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
