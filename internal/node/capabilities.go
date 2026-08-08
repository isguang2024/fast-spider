package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const (
	maxFileReadBytes    = 1 << 20
	maxSearchFileBytes  = 2 << 20
	maxSearchFiles      = 5000
	defaultSearchLimit  = 100
	maxSearchLimit      = 200
	maxSearchLineLength = 500
)

type fileReadParams struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
}

type fileReadResult struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Offset      int64  `json:"offset"`
	BytesRead   int64  `json:"bytesRead"`
	Size        int64  `json:"size"`
	Truncated   bool   `json:"truncated"`
	ChunkSHA256 string `json:"chunkSha256"`
	Encoding    string `json:"encoding"`
}

type codeSearchParams struct {
	Query      string `json:"query"`
	Path       string `json:"path,omitempty"`
	Regex      bool   `json:"regex,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type codeSearchMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

type codeSearchResult struct {
	Matches      []codeSearchMatch `json:"matches"`
	ScannedFiles int               `json:"scannedFiles"`
	Truncated    bool              `json:"truncated"`
}

func (c *Client) handleCapabilityRequest(ctx context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
	response := protocolv1.CapabilityResponse{MessageType: protocolv1.MessageCapabilityResponse, RequestId: req.RequestId, Timestamp: protocolv1.Timestamp(nowUTC())}
	if req.RequestId == "" || req.Capability == "" || req.Action == "" {
		response.Error = protocolError("INVALID_REQUEST", "invalid capability request", false)
		return response
	}
	if req.Deadline != "" {
		deadline, err := parseTimestamp(req.Deadline)
		if err != nil {
			response.Error = protocolError("INVALID_REQUEST", "invalid request deadline", false)
			return response
		}
		if !deadline.After(nowUTC()) {
			response.Error = protocolError("DEADLINE_EXCEEDED", "request deadline exceeded", false)
			return response
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	var result any
	var err error
	switch req.Capability + "/" + req.Action {
	case "workspace.registry/list":
		result, err = c.workspaceList()
	case "file.read/read":
		result, err = c.fileRead(ctx, req.WorkspaceId, req.Params)
	case "code.search/search":
		result, err = c.codeSearch(ctx, req.WorkspaceId, req.Params)
	default:
		response.Error = protocolError("UNSUPPORTED_CAPABILITY", "capability or action is not available", false)
		return response
	}
	if err != nil {
		response.Error = capabilityError(err)
		return response
	}
	raw, err := json.Marshal(result)
	if err != nil {
		response.Error = protocolError("INTERNAL", "failed to encode capability result", true)
		return response
	}
	if err := json.Unmarshal(raw, &response.Result); err != nil {
		response.Error = protocolError("INTERNAL", "failed to normalize capability result", true)
	}
	return response
}

func (c *Client) workspaceList() (map[string]any, error) {
	workspaces, err := NewWorkspaceStore(c.cfg.DataDir).List()
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspaces": workspaces}, nil
}

func (c *Client) fileRead(ctx context.Context, workspaceID string, params map[string]any) (fileReadResult, error) {
	if err := ctx.Err(); err != nil {
		return fileReadResult{}, err
	}
	var input fileReadParams
	if err := decodeParams(params, &input); err != nil {
		return fileReadResult{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.Path == "" {
		return fileReadResult{}, fmt.Errorf("path is required")
	}
	if input.Offset < 0 || input.Limit < 0 {
		return fileReadResult{}, fmt.Errorf("offset and limit must be non-negative")
	}
	if input.Limit == 0 {
		input.Limit = maxFileReadBytes
	}
	if input.Limit > maxFileReadBytes {
		return fileReadResult{}, ErrReadLimit
	}
	workspace, err := NewWorkspaceStore(c.cfg.DataDir).Resolve(workspaceID)
	if err != nil {
		return fileReadResult{}, err
	}
	target, err := resolveWorkspacePath(workspace.Root, input.Path)
	if err != nil {
		return fileReadResult{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return fileReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return fileReadResult{}, ErrNotRegularFile
	}
	probe := make([]byte, 4096)
	probeN, probeErr := file.ReadAt(probe, 0)
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return fileReadResult{}, probeErr
	}
	probe = probe[:probeN]
	if bytes.IndexByte(probe, 0) >= 0 {
		return fileReadResult{}, ErrBinaryOrInvalidUTF8
	}
	if !utf8.Valid(probe) {
		if errors.Is(probeErr, io.EOF) {
			return fileReadResult{}, ErrBinaryOrInvalidUTF8
		}
		if _, ok := trimIncompleteUTF8Suffix(probe); !ok {
			return fileReadResult{}, ErrBinaryOrInvalidUTF8
		}
	}
	if input.Offset > info.Size() {
		input.Offset = info.Size()
	}
	if _, err := file.Seek(input.Offset, io.SeekStart); err != nil {
		return fileReadResult{}, err
	}
	buf := make([]byte, input.Limit+1)
	n, readErr := io.ReadFull(file, buf)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fileReadResult{}, readErr
	}
	truncated := int64(n) > input.Limit
	if truncated {
		n = int(input.Limit)
	}
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return fileReadResult{}, ErrBinaryOrInvalidUTF8
	}
	if !utf8.Valid(buf) {
		if !truncated && input.Offset+int64(len(buf)) >= info.Size() {
			return fileReadResult{}, ErrBinaryOrInvalidUTF8
		}
		var ok bool
		buf, ok = trimIncompleteUTF8Suffix(buf)
		if !ok {
			return fileReadResult{}, ErrBinaryOrInvalidUTF8
		}
	}
	sum := sha256.Sum256(buf)
	return fileReadResult{
		Path: filepath.ToSlash(filepath.Clean(input.Path)), Content: string(buf), Offset: input.Offset,
		BytesRead: int64(len(buf)), Size: info.Size(), Truncated: truncated || input.Offset+int64(len(buf)) < info.Size(),
		ChunkSHA256: "sha256:" + hex.EncodeToString(sum[:]), Encoding: "utf-8",
	}, nil
}

func (c *Client) codeSearch(ctx context.Context, workspaceID string, params map[string]any) (codeSearchResult, error) {
	var input codeSearchParams
	if err := decodeParams(params, &input); err != nil {
		return codeSearchResult{}, fmt.Errorf("invalid params: %w", err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || len(input.Query) > 512 {
		return codeSearchResult{}, fmt.Errorf("query is required and must be at most 512 characters")
	}
	if input.Limit == 0 {
		input.Limit = defaultSearchLimit
	}
	if input.Limit < 1 || input.Limit > maxSearchLimit {
		return codeSearchResult{}, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}
	workspace, err := NewWorkspaceStore(c.cfg.DataDir).Resolve(workspaceID)
	if err != nil {
		return codeSearchResult{}, err
	}
	searchRoot := workspace.Root
	if input.Path != "" && input.Path != "." {
		searchRoot, err = resolveWorkspacePath(workspace.Root, input.Path)
		if err != nil {
			return codeSearchResult{}, err
		}
	}
	info, err := os.Stat(searchRoot)
	if err != nil {
		return codeSearchResult{}, err
	}
	if !info.IsDir() {
		return codeSearchResult{}, fmt.Errorf("search path must be a directory")
	}
	matcher, err := compileSearchMatcher(input.Query, input.Regex, input.IgnoreCase)
	if err != nil {
		return codeSearchResult{}, err
	}
	result := codeSearchResult{Matches: []codeSearchMatch{}}
	err = filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != searchRoot && shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
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
		matches, binary, err := searchFile(path, workspace.Root, matcher, input.Limit-len(result.Matches))
		if err != nil || binary {
			return nil
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

type lineMatcher func(string) (int, bool)

func compileSearchMatcher(query string, useRegex, ignoreCase bool) (lineMatcher, error) {
	if useRegex {
		pattern := query
		if ignoreCase {
			pattern = "(?i)" + pattern
		}
		rx, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		return func(line string) (int, bool) {
			loc := rx.FindStringIndex(line)
			if loc == nil {
				return 0, false
			}
			return loc[0] + 1, true
		}, nil
	}
	needle := query
	if ignoreCase {
		needle = strings.ToLower(needle)
	}
	return func(line string) (int, bool) {
		haystack := line
		if ignoreCase {
			haystack = strings.ToLower(line)
		}
		index := strings.Index(haystack, needle)
		return index + 1, index >= 0
	}, nil
}

func searchFile(path, searchRoot string, matcher lineMatcher, remaining int) ([]codeSearchMatch, bool, error) {
	if remaining <= 0 {
		return nil, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	probe := make([]byte, 4096)
	n, err := file.Read(probe)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	probe = probe[:n]
	if bytes.IndexByte(probe, 0) >= 0 || !utf8.Valid(probe) {
		return nil, true, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}
	rel, err := filepath.Rel(searchRoot, path)
	if err != nil {
		return nil, false, err
	}
	var matches []codeSearchMatch
	scanner := bufio.NewScanner(io.LimitReader(file, maxSearchFileBytes+1))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		column, ok := matcher(line)
		if !ok {
			continue
		}
		text := line
		if len(text) > maxSearchLineLength {
			text = text[:maxSearchLineLength]
		}
		matches = append(matches, codeSearchMatch{Path: filepath.ToSlash(rel), Line: lineNo, Column: column, Text: text})
		if len(matches) >= remaining {
			break
		}
	}
	return matches, false, scanner.Err()
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", "vendor", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func trimIncompleteUTF8Suffix(input []byte) ([]byte, bool) {
	if utf8.Valid(input) {
		return input, true
	}
	for trim := 1; trim <= 3 && trim <= len(input); trim++ {
		candidate := input[:len(input)-trim]
		if utf8.Valid(candidate) {
			return candidate, true
		}
	}
	return nil, false
}

func decodeParams(input map[string]any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

var (
	ErrReadLimit           = errors.New("read limit exceeded")
	ErrNotRegularFile      = errors.New("not a regular file")
	ErrBinaryOrInvalidUTF8 = errors.New("binary or invalid utf-8 file")
)

func capabilityError(err error) *protocolv1.ProtocolError {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return protocolError("DEADLINE_EXCEEDED", "request deadline exceeded or canceled", true)
	case errors.Is(err, ErrWorkspaceNotFound):
		return protocolError("WORKSPACE_NOT_FOUND", "workspace was not found", false)
	case errors.Is(err, ErrWorkspaceDisabled):
		return protocolError("WORKSPACE_DISABLED", "workspace is disabled", false)
	case errors.Is(err, ErrPathOutsideWorkspace):
		return protocolError("PATH_OUTSIDE_WORKSPACE", "path is outside the authorized workspace", false)
	case errors.Is(err, os.ErrNotExist):
		return protocolError("NOT_FOUND", "path was not found", false)
	case errors.Is(err, ErrReadLimit):
		return protocolError("OUTPUT_LIMIT", "requested read exceeds the allowed limit", false)
	case errors.Is(err, ErrNotRegularFile):
		return protocolError("NOT_REGULAR_FILE", "path is not a regular file", false)
	case errors.Is(err, ErrBinaryOrInvalidUTF8):
		return protocolError("NOT_TEXT", "file is binary or not valid utf-8", false)
	default:
		return protocolError("INVALID_REQUEST", "capability request could not be completed", false)
	}
}

func protocolError(code, message string, retryable bool) *protocolv1.ProtocolError {
	return &protocolv1.ProtocolError{Code: code, Message: message, Retryable: retryable}
}

func parseTimestamp(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
func nowUTC() time.Time                              { return time.Now().UTC() }
