package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const (
	maxFileReadBytes    = 128 << 10
	maxSearchFileBytes  = 2 << 20
	maxSearchFiles      = 5000
	defaultSearchLimit  = 100
	maxSearchLimit      = 200
	maxSearchLineLength = 500
)

type fileReadParams struct {
	Path               string `json:"path"`
	Offset             int64  `json:"offset,omitempty"`
	Limit              int64  `json:"limit,omitempty"`
	LineStart          int    `json:"lineStart,omitempty"`
	LineCount          int    `json:"lineCount,omitempty"`
	HeadLines          int    `json:"headLines,omitempty"`
	TailLines          int    `json:"tailLines,omitempty"`
	AroundLine         int    `json:"aroundLine,omitempty"`
	ContextLines       int    `json:"contextLines,omitempty"`
	StatOnly           bool   `json:"statOnly,omitempty"`
	IncludeLineNumbers bool   `json:"includeLineNumbers,omitempty"`
}

type fileReadResult struct {
	Path            string  `json:"path"`
	Content         *string `json:"content,omitempty"`
	Offset          int64   `json:"offset"`
	BytesRead       int64   `json:"bytesRead"`
	SourceBytesRead int64   `json:"sourceBytesRead,omitempty"`
	Size            int64   `json:"size"`
	LineStart       int     `json:"lineStart,omitempty"`
	LineEnd         int     `json:"lineEnd,omitempty"`
	StatOnly        bool    `json:"statOnly,omitempty"`
	Truncated       bool    `json:"truncated"`
	ChunkSHA256     string  `json:"chunkSha256,omitempty"`
	FileSHA256      string  `json:"fileSha256"`
	Encoding        string  `json:"encoding"`
}

type codeSearchParams struct {
	Query         string   `json:"query"`
	Path          string   `json:"path,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	Regex         bool     `json:"regex,omitempty"`
	IgnoreCase    bool     `json:"ignoreCase,omitempty"`
	Include       []string `json:"include,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	Context       int      `json:"context,omitempty"`
	BeforeContext int      `json:"beforeContext,omitempty"`
	AfterContext  int      `json:"afterContext,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

type codeSearchContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type codeSearchMatch struct {
	Path   string                  `json:"path"`
	Line   int                     `json:"line"`
	Column int                     `json:"column"`
	Text   string                  `json:"text"`
	Before []codeSearchContextLine `json:"before,omitempty"`
	After  []codeSearchContextLine `json:"after,omitempty"`
}

type codeSearchResult struct {
	Matches           []codeSearchMatch `json:"matches"`
	Files             []string          `json:"files,omitempty"`
	ScannedFiles      int               `json:"scannedFiles"`
	MatchedFiles      int               `json:"matchedFiles"`
	BytesScanned      int64             `json:"bytesScanned"`
	MatchCount        int               `json:"matchCount"`
	SkippedFiles      int               `json:"skippedFiles,omitempty"`
	SkipReasons       map[string]int    `json:"skipReasons,omitempty"`
	Incomplete        bool              `json:"incomplete,omitempty"`
	Engine            string            `json:"engine"`
	FallbackReason    string            `json:"fallbackReason,omitempty"`
	PrimaryElapsedMs  int64             `json:"primaryElapsedMs"`
	FallbackElapsedMs int64             `json:"fallbackElapsedMs"`
	ElapsedMs         int64             `json:"elapsedMs"`
	Truncated         bool              `json:"truncated"`
}

func (c *Client) handleCapabilityRequest(ctx context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
	started := time.Now()
	response := protocolv1.CapabilityResponse{MessageType: protocolv1.MessageCapabilityResponse, RequestId: req.RequestId, TraceId: req.TraceId, Timestamp: protocolv1.Timestamp(nowUTC())}
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
	case "file.read/read":
		result, err = c.fileRead(ctx, req.Params)
	case "file.write/edit", "file.write/create", "file.write/replace", "file.write/editMany", "file.write/preview":
		result, err = c.fileEdit(ctx, req.Action, req.Params)
	case "code.search/search":
		result, err = c.codeSearch(ctx, req.Params)
	case "shell.exec/run":
		result, err = c.shellRun(ctx, req.RequestId, req.TraceId, req.Params)
	case "job.control/watch":
		result, err = c.jobWatch(ctx, req.Params)
	case "job.control/cancel":
		result, err = c.jobCancel(ctx, req.Params)
	case "git.repository/status", "git.repository/diff", "git.repository/stagedDiff", "git.repository/log", "git.repository/show", "git.repository/branches", "git.repository/currentBranch", "git.repository/worktrees", "git.repository/add", "git.repository/commit", "git.repository/fetch", "git.repository/pull", "git.repository/push", "git.repository/createWorktree", "git.repository/deleteWorktree":
		params := cloneParams(req.Params)
		params["action"] = req.Action
		result, err = c.gitControl(ctx, params)
	case "build.exec/run":
		params := cloneParams(req.Params)
		params["action"] = req.Action
		result, err = c.buildControl(ctx, req.RequestId, req.TraceId, params)
	case "artifact.store/uploadFile":
		result, err = c.artifactUploadFile(ctx, req.Params)
	case "artifact.store/uploadJobLog":
		result, err = c.artifactUploadJobLog(ctx, req.Params)
	case "artifact.store/publishFile":
		result, err = c.presentationPublishFile(ctx, req.Params)
	case "working.context/get", "working.context/set", "working.context/clear",
		"working.context/plan.init", "working.context/plan.get", "working.context/plan.list", "working.context/plan.sync",
		"working.context/task.update", "working.context/markdown.list", "working.context/markdown.read", "working.context/markdown.append", "working.context/progress.watch":
		result, err = c.workingContextControl(ctx, req.Action, req.Params)
	case "browser.automation/readiness", "browser.automation/launch", "browser.automation/close", "browser.automation/page.open", "browser.automation/page.navigate", "browser.automation/page.close", "browser.automation/pages.list", "browser.automation/click", "browser.automation/type", "browser.automation/press", "browser.automation/wait", "browser.automation/batch", "browser.automation/snapshot", "browser.automation/screenshot", "browser.automation/events":
		result, err = c.browserControl(ctx, req.Action, req.Params)
	case "screenshot.capture/listDisplays", "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/listWindows", "screenshot.capture/window":
		result, err = c.screenshotCapture(ctx, req.Action, req.Params)
	case "agent.control/routing.status", "agent.control/providers.list", "agent.control/provider.readiness", "agent.control/models.list", "agent.control/provider.capabilities", "agent.control/projects.list", "agent.control/skills.list", "agent.control/hooks.list", "agent.control/permissions.list", "agent.control/plugins.list", "agent.control/plugins.installed", "agent.control/plugins.get", "agent.control/plugin.skill.read", "agent.control/mcp.status.list", "agent.control/session.list", "agent.control/session.get", "agent.control/session.create", "agent.control/session.send", "agent.control/session.steer", "agent.control/session.respond", "agent.control/session.watch", "agent.control/session.cancel", "agent.control/session.result", "agent.control/session.rename", "agent.control/session.archive", "agent.control/session.unarchive", "agent.control/session.delete", "agent.control/session.fork", "agent.control/session.compact", "agent.control/session.rollback", "agent.control/session.goal.get", "agent.control/session.goal.set", "agent.control/session.goal.clear", "agent.control/session.settings.update", "agent.control/session.review":
		if c.agent == nil {
			err = ErrAgentProviderUnavailable
		} else {
			result, err = c.agent.Control(ctx, req.Action, req.Params)
		}
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
		return response
	}
	timing, _ := response.Result["timing"].(map[string]any)
	if timing == nil {
		timing = map[string]any{}
	}
	timing["nodeExecutionMs"] = time.Since(started).Milliseconds()
	response.Result["timing"] = timing
	return response
}

func (c *Client) fileRead(ctx context.Context, params map[string]any) (fileReadResult, error) {
	return c.fileReadV2(ctx, params)
}

func (c *Client) codeSearch(ctx context.Context, params map[string]any) (codeSearchResult, error) {
	return c.codeSearchV2(ctx, params)
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

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", "vendor", ".idea", ".vscode", ".next", ".nuxt", ".cache", "coverage", "dist", "build", "out", "target", "bin", "obj":
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

func cloneParams(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	return out
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
	var hubErr *HubAPIError
	if errors.As(err, &hubErr) {
		return protocolError(hubErr.Code, hubErr.Message, hubErr.Retryable)
	}
	var browserErr *BrowserActionError
	if errors.As(err, &browserErr) {
		return protocolError(browserErr.Code, browserErr.Message, browserErr.Retryable)
	}
	var agentErr AgentCapabilityError
	if errors.As(err, &agentErr) {
		code, message, retryable := agentErr.CapabilityError()
		return protocolError(code, message, retryable)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return protocolError("DEADLINE_EXCEEDED", "request deadline exceeded or canceled", true)
	case errors.Is(err, ErrAbsolutePathRequired):
		return protocolError("ABSOLUTE_PATH_REQUIRED", "an absolute local path is required", false)
	case errors.Is(err, os.ErrNotExist):
		return protocolError("NOT_FOUND", "path was not found", false)
	case errors.Is(err, ErrReadLimit):
		return protocolError("OUTPUT_LIMIT", "requested operation exceeds the allowed size limit", false)
	case errors.Is(err, ErrPermissionDenied):
		return protocolError("PERMISSION_DENIED", "the operating system denied this operation", false)
	case errors.Is(err, ErrRevisionConflict):
		result := protocolError("REVISION_CONFLICT", "file changed since it was read", false)
		var conflict *FileRevisionError
		if errors.As(err, &conflict) {
			result.Details = map[string]any{"path": conflict.Path, "expectedSha256": conflict.Expected, "actualSha256": conflict.Actual}
		}
		return result
	case errors.Is(err, ErrEditNotUnique):
		return protocolError("EDIT_NOT_UNIQUE", "oldText must match exactly once", false)
	case errors.Is(err, ErrEditOverlap):
		return protocolError("EDIT_OVERLAP", "edit ranges must not overlap", false)
	case errors.Is(err, ErrFileAlreadyExists):
		return protocolError("ALREADY_EXISTS", "file already exists", false)
	case errors.Is(err, ErrJobNotFound):
		return protocolError("JOB_NOT_FOUND", "job was not found", false)
	case errors.Is(err, ErrJobNotComplete):
		return protocolError("JOB_NOT_COMPLETE", "job must be terminal before exporting its log", true)
	case errors.Is(err, ErrJobLogUnavailable):
		return protocolError("JOB_LOG_UNAVAILABLE", "job log is unavailable", false)
	case errors.Is(err, ErrJobLimit):
		return protocolError("RESOURCE_LIMIT", "job resource limit reached", true)
	case errors.Is(err, ErrRuntimeUnavailable):
		return protocolError("RUNTIME_UNAVAILABLE", "requested execution runtime is unavailable", false)
	case errors.Is(err, ErrWSLCwdUnmappable):
		return protocolError("WSL_CWD_UNMAPPABLE", "Windows cwd cannot be mapped inside the selected WSL distribution", false)
	case errors.Is(err, ErrIdempotencyConflict):
		return protocolError("IDEMPOTENCY_CONFLICT", "idempotency key was reused with different parameters", false)
	case errors.Is(err, ErrGitNotFound):
		return protocolError("GIT_NOT_FOUND", "system Git is not available", false)
	case errors.Is(err, ErrNotRepository):
		return protocolError("NOT_A_REPOSITORY", "repositoryPath is not a Git repository root", false)
	case errors.Is(err, ErrGitHooksDenied):
		return protocolError("GIT_HOOKS_DISABLED", "active Git hooks or executable Git filters require local git-hooks permission", false)
	case errors.Is(err, ErrGitOutputTooLarge):
		return protocolError("OUTPUT_LIMIT", "Git output exceeds the inline limit", false)
	case errors.Is(err, ErrBrowserUnavailable):
		return protocolError("BROWSER_UNAVAILABLE", "browser sidecar or managed browser runtime is not installed", false)
	case errors.Is(err, ErrWindowTokenInvalid):
		return protocolError("WINDOW_NOT_FOUND", "window ID is invalid or expired", false)
	case errors.Is(err, ErrAgentProviderUnavailable):
		return protocolError("AGENT_PROVIDER_UNAVAILABLE", "agent provider is unavailable on this Node", true)
	case errors.Is(err, ErrAgentSessionNotFound):
		return protocolError("AGENT_SESSION_NOT_FOUND", "agent session was not found on this Node", false)
	case errors.Is(err, ErrAgentSessionBusy):
		return protocolError("AGENT_SESSION_BUSY", "agent session already has an active run", true)
	case errors.Is(err, ErrScreenshotUnavailable):
		return protocolError("SCREENSHOT_UNAVAILABLE", "screenshot capture is unavailable in the current graphical session", false)
	case errors.Is(err, ErrScreenshotTooLarge):
		return protocolError("SCREENSHOT_TOO_LARGE", "desktop screenshot exceeds the configured resource limit", false)
	case errors.Is(err, ErrPresentationUpload):
		return protocolError("PRESENTATION_UPLOAD_FAILED", "failed to publish the generated resource for AI presentation", true)
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
