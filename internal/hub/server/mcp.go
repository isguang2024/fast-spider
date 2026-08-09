package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type machineListInput struct{}

type machineGetInput struct {
	MachineID string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
}

type capabilityListInput struct {
	MachineID string `json:"machineId,omitempty" jsonschema:"optional machine ID; omit for the Hub capability catalog"`
}

type workspaceListInput struct {
	MachineID string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
}

type fileReadInput struct {
	MachineID   string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID string `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Path        string `json:"path" jsonschema:"relative path inside the authorized workspace; absolute paths are rejected"`
	Offset      int64  `json:"offset,omitempty" jsonschema:"byte offset, default 0"`
	Limit       int64  `json:"limit,omitempty" jsonschema:"maximum bytes to return, default and maximum 131072; use offset for larger files"`
}

type codeSearchInput struct {
	MachineID   string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID string `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Query       string `json:"query" jsonschema:"literal text or regular expression to search"`
	Path        string `json:"path,omitempty" jsonschema:"optional relative directory inside the workspace"`
	Regex       bool   `json:"regex,omitempty" jsonschema:"interpret query as a Go regular expression"`
	IgnoreCase  bool   `json:"ignoreCase,omitempty" jsonschema:"case-insensitive matching"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum matches, default 100 and maximum 200"`
}

type fileEditInput struct {
	MachineID          string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID        string `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Path               string `json:"path" jsonschema:"relative path inside the authorized workspace"`
	OldText            string `json:"oldText" jsonschema:"text that must occur exactly once"`
	NewText            string `json:"newText" jsonschema:"replacement text"`
	ExpectedFileSHA256 string `json:"expectedFileSha256" jsonschema:"full file SHA-256 from file_read; required for optimistic concurrency"`
}

type shellRunInput struct {
	MachineID      string   `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID    string   `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Argv           []string `json:"argv" jsonschema:"explicit executable and arguments; no implicit shell interpolation"`
	Cwd            string   `json:"cwd,omitempty" jsonschema:"relative working directory inside the workspace"`
	TimeoutSeconds int64    `json:"timeoutSeconds,omitempty" jsonschema:"0 uses the default; maximum 1800 seconds"`
	IdempotencyKey string   `json:"idempotencyKey" jsonschema:"12-128 character key preventing duplicate process starts on retries"`
}

type jobWatchInput struct {
	MachineID   string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID string `json:"workspaceId" jsonschema:"workspace that owns the job"`
	JobID       string `json:"jobId" jsonschema:"opaque job ID returned by shell_run/build_control/git_control"`
	Cursor      int64  `json:"cursor,omitempty" jsonschema:"last consumed event sequence"`
	WaitSeconds int64  `json:"waitSeconds,omitempty" jsonschema:"long-poll wait from 0 to 15 seconds"`
}

type jobCancelInput struct {
	MachineID   string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID string `json:"workspaceId" jsonschema:"workspace that owns the job"`
	JobID       string `json:"jobId" jsonschema:"opaque job ID returned by shell_run/build_control/git_control"`
}

type gitControlInput struct {
	MachineID           string   `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID         string   `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Action              string   `json:"action" jsonschema:"one of status,diff,stagedDiff,log,show,branches,currentBranch,worktrees,add,commit,fetch,pull,push,createWorktree,deleteWorktree"`
	Revision            string   `json:"revision,omitempty" jsonschema:"revision for show"`
	Paths               []string `json:"paths,omitempty" jsonschema:"relative paths for add"`
	Message             string   `json:"message,omitempty" jsonschema:"commit message"`
	Remote              string   `json:"remote,omitempty" jsonschema:"configured remote name for network actions"`
	Branch              string   `json:"branch,omitempty" jsonschema:"branch or ref for network/worktree actions"`
	WorktreeWorkspaceID string   `json:"worktreeWorkspaceId,omitempty" jsonschema:"managed worktree workspace ID for deleteWorktree"`
	IdempotencyKey      string   `json:"idempotencyKey,omitempty" jsonschema:"required for network actions"`
}

type buildControlInput struct {
	MachineID      string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID    string `json:"workspaceId" jsonschema:"opaque Node-authorized workspace ID"`
	Action         string `json:"action" jsonschema:"list or run"`
	ProfileID      string `json:"profileId,omitempty" jsonschema:"locally configured profile ID for run"`
	IdempotencyKey string `json:"idempotencyKey,omitempty" jsonschema:"required for run"`
}

type artifactGetInput struct {
	Action      string `json:"action" jsonschema:"get, uploadFile, or uploadJobLog"`
	ArtifactID  string `json:"artifactId,omitempty" jsonschema:"artifact ID for get"`
	MachineID   string `json:"machineId,omitempty" jsonschema:"machine ID for upload actions"`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"workspace ID for upload actions"`
	Path        string `json:"path,omitempty" jsonschema:"relative workspace file path for uploadFile"`
	JobID       string `json:"jobId,omitempty" jsonschema:"terminal job ID for uploadJobLog"`
	LogicalName string `json:"logicalName,omitempty" jsonschema:"artifact display file name"`
	ContentType string `json:"contentType,omitempty" jsonschema:"optional MIME type for uploadFile"`
}

type browserLocatorInput struct {
	Role   string `json:"role,omitempty" jsonschema:"accessible role locator"`
	Name   string `json:"name,omitempty" jsonschema:"accessible name used with role"`
	Label  string `json:"label,omitempty" jsonschema:"form label locator"`
	Text   string `json:"text,omitempty" jsonschema:"visible text locator"`
	TestID string `json:"testId,omitempty" jsonschema:"test id locator"`
	CSS    string `json:"css,omitempty" jsonschema:"bounded CSS locator; XPath is not supported"`
	Exact  bool   `json:"exact,omitempty" jsonschema:"require exact text/name match"`
}

type browserControlInput struct {
	MachineID        string               `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID      string               `json:"workspaceId" jsonschema:"enabled Node-authorized workspace; local/private browser origins are configured locally when needed"`
	Action           string               `json:"action" jsonschema:"launch,close,page.open,page.navigate,page.close,pages.list,click,type,press,wait,snapshot,screenshot,events"`
	BrowserSessionID string               `json:"browserSessionId,omitempty" jsonschema:"opaque managed browser session ID"`
	PageID           string               `json:"pageId,omitempty" jsonschema:"opaque managed page ID"`
	Engine           string               `json:"engine,omitempty" jsonschema:"chromium in the Phase 5 MVP"`
	Headed           bool                 `json:"headed,omitempty" jsonschema:"show the isolated managed browser window instead of headless mode"`
	ViewportWidth    int                  `json:"viewportWidth,omitempty" jsonschema:"viewport width from 320 to 2560"`
	ViewportHeight   int                  `json:"viewportHeight,omitempty" jsonschema:"viewport height from 240 to 1600"`
	URL              string               `json:"url,omitempty" jsonschema:"http(s) URL; public targets work directly, local/private targets require a locally configured persistent origin"`
	WaitUntil        string               `json:"waitUntil,omitempty" jsonschema:"load,domcontentloaded,networkidle, or commit"`
	Locator          *browserLocatorInput `json:"locator,omitempty" jsonschema:"structured locator for click/type/press/wait"`
	Text             string               `json:"text,omitempty" jsonschema:"text for type"`
	Key              string               `json:"key,omitempty" jsonschema:"key for press"`
	State            string               `json:"state,omitempty" jsonschema:"attached,detached,visible,hidden for wait"`
	TimeoutSeconds   int                  `json:"timeoutSeconds,omitempty" jsonschema:"action timeout from 1 to 30 seconds"`
	FullPage         bool                 `json:"fullPage,omitempty" jsonschema:"capture the full page when within pixel limits"`
	Format           string               `json:"format,omitempty" jsonschema:"png or jpeg"`
	Quality          int                  `json:"quality,omitempty" jsonschema:"jpeg quality 20-95"`
	Cursor           int64                `json:"cursor,omitempty" jsonschema:"last browser event cursor for events"`
}

type screenshotTakeInput struct {
	MachineID    string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID  string `json:"workspaceId" jsonschema:"enabled workspace used to scope the request and resulting Artifact"`
	Action       string `json:"action" jsonschema:"listDisplays, desktop, display, listWindows, or window"`
	DisplayIndex int    `json:"displayIndex,omitempty" jsonschema:"zero-based active display index for action=display"`
	WindowID     string `json:"windowId,omitempty" jsonschema:"opaque short-lived window ID returned by listWindows for action=window"`
	Format       string `json:"format,omitempty" jsonschema:"png or jpeg; png is the default"`
	Quality      int    `json:"quality,omitempty" jsonschema:"jpeg quality 20-95"`
}

type aiControlInput struct {
	MachineID        string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	WorkspaceID      string `json:"workspaceId,omitempty" jsonschema:"workspace for project/session actions; omit for providers/models/projects discovery"`
	Action           string `json:"action" jsonschema:"providers.list,models.list,projects.list,session.list,session.get,session.create,session.send,session.watch,session.cancel,session.result,session.rename,session.archive"`
	ProviderID       string `json:"providerId,omitempty" jsonschema:"provider ID; defaults to codex"`
	SessionID        string `json:"sessionId,omitempty" jsonschema:"opaque provider session ID"`
	TurnID           string `json:"turnId,omitempty" jsonschema:"active turn ID for cancel when known"`
	Prompt           string `json:"prompt,omitempty" jsonschema:"prompt for session.create/session.send"`
	WorkingDirectory string `json:"workingDirectory,omitempty" jsonschema:"relative directory inside the authorized Workspace"`
	Model            string `json:"model,omitempty" jsonschema:"optional provider model ID"`
	Thinking         string `json:"thinking,omitempty" jsonschema:"optional provider reasoning effort"`
	Cursor           int64  `json:"cursor,omitempty" jsonschema:"last consumed normalized event sequence"`
	WaitSeconds      int64  `json:"waitSeconds,omitempty" jsonschema:"session.watch long-poll from 0 to 15 seconds"`
	Limit            int    `json:"limit,omitempty" jsonschema:"session.list maximum, default 50 and maximum 100"`
	Name             string `json:"name,omitempty" jsonschema:"new session name for session.rename"`
}

type genericCapabilityOutput struct {
	Result map[string]any `json:"result"`
}

type mcpMachine struct {
	MachineID            string                            `json:"machineId"`
	DisplayName          string                            `json:"displayName"`
	AdminNote            string                            `json:"adminNote,omitempty"`
	Status               string                            `json:"status"`
	Online               bool                              `json:"online"`
	RuntimeStatus        string                            `json:"runtimeStatus,omitempty"`
	OS                   string                            `json:"os"`
	Arch                 string                            `json:"arch"`
	NodeVersion          string                            `json:"nodeVersion"`
	Generation           int64                             `json:"generation"`
	LastSeenAt           string                            `json:"lastSeenAt,omitempty"`
	RegistrationMode     string                            `json:"registrationMode"`
	ConfigurationScope   string                            `json:"configurationScope"`
	RuntimeCredential    string                            `json:"runtimeCredential"`
	ConnectionTokenSaved bool                              `json:"connectionTokenSaved"`
	Capabilities         []protocolv1.CapabilityDescriptor `json:"capabilities,omitempty"`
}

type machineListOutput struct {
	Machines []mcpMachine `json:"machines"`
}

type machineGetOutput struct {
	Machine mcpMachine `json:"machine"`
}

type capabilityListOutput struct {
	Capabilities []protocolv1.CapabilityDescriptor `json:"capabilities"`
}

type workspaceListOutput struct {
	Workspaces []protocolv1.WorkspaceSummary `json:"workspaces"`
}

type fileReadOutput struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Offset      int64  `json:"offset"`
	BytesRead   int64  `json:"bytesRead"`
	Size        int64  `json:"size"`
	Truncated   bool   `json:"truncated"`
	ChunkSHA256 string `json:"chunkSha256"`
	FileSHA256  string `json:"fileSha256,omitempty"`
	Encoding    string `json:"encoding"`
}

type fileEditOutput struct {
	Path          string `json:"path"`
	BeforeSHA256  string `json:"beforeSha256"`
	AfterSHA256   string `json:"afterSha256"`
	Bytes         int64  `json:"bytes"`
	Diff          string `json:"diff"`
	DiffTruncated bool   `json:"diffTruncated"`
}

type mcpJobEvent struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Timestamp string `json:"timestamp"`
}

type jobOutput struct {
	JobID           string        `json:"jobId"`
	State           string        `json:"state"`
	ExitCode        *int          `json:"exitCode,omitempty"`
	Error           string        `json:"error,omitempty"`
	Events          []mcpJobEvent `json:"events"`
	NextCursor      int64         `json:"nextCursor"`
	TruncatedBefore int64         `json:"truncatedBefore,omitempty"`
	StartedAt       string        `json:"startedAt"`
	FinishedAt      string        `json:"finishedAt,omitempty"`
}

type codeSearchMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

type codeSearchOutput struct {
	Matches      []codeSearchMatch `json:"matches"`
	ScannedFiles int               `json:"scannedFiles"`
	Truncated    bool              `json:"truncated"`
}

func (s *Server) newMCPHandler() http.Handler {
	base := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		tokenInfo := auth.TokenInfoFromContext(r.Context())
		if tokenInfo == nil || tokenInfo.UserID == "" {
			return nil
		}
		return s.mcpServerFor(tokenInfo.UserID)
	}, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
		Stateless:    true,
	})
	limited := http.MaxBytesHandler(base, maxControlMessageBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataURL, err := s.oauthResourceMetadataURL(r)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}
		middleware := auth.RequireBearerToken(s.mcpTokenVerifier, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURL,
			Scopes:              []string{oauthScope},
		})
		middleware(limited).ServeHTTP(w, r)
	})
}

func (s *Server) mcpServerFor(ownerID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "fast-spider", Title: "Fast Spider", Version: s.service.Version()}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_list",
		Description: "List Fast Spider machines owned by the authenticated owner, including online state, connection-token registration mode, local configuration scope, runtime credential mode and negotiated capabilities. Connection-token secrets are never returned.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ machineListInput) (*mcp.CallToolResult, machineListOutput, error) {
		machines, err := s.service.ListMachines(ctx, ownerID)
		if err != nil {
			return nil, machineListOutput{}, err
		}
		out := machineListOutput{Machines: make([]mcpMachine, 0, len(machines))}
		for _, machine := range machines {
			out.Machines = append(out.Machines, toMCPMachine(machine))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_get",
		Description: "Get one Fast Spider machine by opaque machineId, including its registration/runtime credential model. This never returns connection-token secrets or accepts a local filesystem path.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input machineGetInput) (*mcp.CallToolResult, machineGetOutput, error) {
		machine, err := s.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, machineGetOutput{}, err
		}
		return nil, machineGetOutput{Machine: toMCPMachine(machine)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "capability_list",
		Description: "List the fixed Fast Spider capability catalog, or capabilities currently reported by a specific machine.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input capabilityListInput) (*mcp.CallToolResult, capabilityListOutput, error) {
		if input.MachineID == "" {
			return nil, capabilityListOutput{Capabilities: s.service.CapabilityCatalog()}, nil
		}
		machine, err := s.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, capabilityListOutput{}, err
		}
		return nil, capabilityListOutput{Capabilities: machine.Capabilities}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "workspace_list",
		Description: "List Node-authorized workspaces by opaque workspaceId. Local absolute paths are never returned through this remote tool.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workspaceListInput) (*mcp.CallToolResult, workspaceListOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "", "workspace.registry", "list", map[string]any{})
		if err != nil {
			return nil, workspaceListOutput{}, err
		}
		var out workspaceListOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, workspaceListOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_read",
		Description: "Read UTF-8 text from a relative path inside a Node-authorized workspace. Absolute paths and workspace escapes are rejected by the Node.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input fileReadInput) (*mcp.CallToolResult, fileReadOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "file.read", "read", map[string]any{"path": input.Path, "offset": input.Offset, "limit": input.Limit})
		if err != nil {
			return nil, fileReadOutput{}, err
		}
		var out fileReadOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, fileReadOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "code_search",
		Description: "Search text files inside a Node-authorized workspace with bounded files, file sizes, matches and request deadline.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input codeSearchInput) (*mcp.CallToolResult, codeSearchOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "code.search", "search", map[string]any{"query": input.Query, "path": input.Path, "regex": input.Regex, "ignoreCase": input.IgnoreCase, "limit": input.Limit})
		if err != nil {
			return nil, codeSearchOutput{}, err
		}
		var out codeSearchOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, codeSearchOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "file_edit",
		Description: "Perform one exact optimistic-concurrency text replacement inside a Node-authorized workspace. Write permission must be enabled locally on the Node.",
		Annotations: toolAnnotations(false, true, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input fileEditInput) (*mcp.CallToolResult, fileEditOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "file.write", "edit", map[string]any{"path": input.Path, "oldText": input.OldText, "newText": input.NewText, "expectedFileSha256": input.ExpectedFileSHA256})
		if err != nil {
			return nil, fileEditOutput{}, err
		}
		var out fileEditOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, fileEditOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "shell_run",
		Description: "Start a bounded non-interactive process in a Node-authorized workspace using an explicit argv array. Shell permission must be enabled locally on the Node.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input shellRunInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "shell.exec", "run", map[string]any{"argv": input.Argv, "cwd": input.Cwd, "timeoutSeconds": input.TimeoutSeconds, "idempotencyKey": input.IdempotencyKey})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_watch",
		Description: "Read bounded stdout/stderr/status events for one Node job after a cursor, optionally long-polling for up to 15 seconds.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobWatchInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "job.control", "watch", map[string]any{"jobId": input.JobID, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_cancel",
		Description: "Cancel one active Node job and terminate its process tree. Repeated cancellation of a terminal job is safe.",
		Annotations: toolAnnotations(false, true, true, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobCancelInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "job.control", "cancel", map[string]any{"jobId": input.JobID})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "git_control",
		Description: "Run one allowlisted system-Git action inside an authorized repository. Git write, network, and hook execution are separately controlled by local Node permissions.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input gitControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "git.repository", input.Action, map[string]any{
			"revision": input.Revision, "paths": input.Paths, "message": input.Message, "remote": input.Remote,
			"branch": input.Branch, "worktreeWorkspaceId": input.WorktreeWorkspaceID, "idempotencyKey": input.IdempotencyKey,
		})
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "build_control",
		Description: "List or run a build/test profile that was configured locally on the Node. Remote callers cannot supply an arbitrary build command.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input buildControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "build.profile", input.Action, map[string]any{"profileId": input.ProfileID, "idempotencyKey": input.IdempotencyKey})
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "browser_control",
		Description: "Control one Node-managed isolated Chromium session with fixed actions. Public web targets work directly; local/private targets use a Node-local persistent origin allowlist. It never attaches to the user's normal browser profile or exposes raw CDP/Playwright execution.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input browserControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := browserControlParams(input)
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "browser.automation", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "screenshot_take",
		Description: "Capture a one-time desktop, display, or window image on a Node for an enabled workspace. Use listDisplays/listWindows for targets; results are Hub Artifacts and never local paths.",
		Annotations: toolAnnotations(false, false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input screenshotTakeInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := map[string]any{"displayIndex": input.DisplayIndex, "windowId": input.WindowID, "format": input.Format, "quality": input.Quality}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "screenshot.capture", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ai_control",
		Description: "Discover local AI providers/models/projects and control provider sessions through the Node. Phase 6 currently implements bridge-owned Codex sessions only; it reuses the authorized Workspace and never sends provider credentials to Hub.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input aiControlInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		params := map[string]any{
			"providerId": input.ProviderID, "sessionId": input.SessionID, "turnId": input.TurnID,
			"prompt": input.Prompt, "workingDirectory": input.WorkingDirectory, "model": input.Model,
			"thinking": input.Thinking, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds,
			"limit": input.Limit, "name": input.Name,
		}
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "agent.control", input.Action, params)
		if err != nil {
			return nil, genericCapabilityOutput{}, err
		}
		return nil, genericCapabilityOutput{Result: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "artifact_get",
		Description: "Get Artifact metadata/content or ask a Node to upload an authorized workspace file or terminal Job log into Hub Artifact storage. Raw chunk upload remains an internal Node protocol.",
		Annotations: toolAnnotations(false, false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input artifactGetInput) (*mcp.CallToolResult, genericCapabilityOutput, error) {
		switch input.Action {
		case "uploadFile", "uploadJobLog":
			params := map[string]any{"path": input.Path, "jobId": input.JobID, "logicalName": input.LogicalName, "contentType": input.ContentType}
			result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, input.WorkspaceID, "artifact.store", input.Action, params)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			return nil, genericCapabilityOutput{Result: result}, nil
		case "get":
			artifact, err := s.service.GetArtifact(ctx, ownerID, input.ArtifactID)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			raw, err := json.Marshal(artifact)
			if err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, genericCapabilityOutput{}, err
			}
			result["downloadPath"] = "/api/v1/artifacts/" + artifact.ID + "/content"
			if content, ok, err := readArtifactInline(ctx, s.service, artifact); err != nil {
				return nil, genericCapabilityOutput{}, err
			} else if ok {
				result["content"] = content
				result["encoding"] = "utf-8"
			}
			return nil, genericCapabilityOutput{Result: result}, nil
		default:
			return nil, genericCapabilityOutput{}, fmt.Errorf("unsupported artifact action %q", input.Action)
		}
	})

	return server
}

func toolAnnotations(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}

func browserControlParams(input browserControlInput) map[string]any {
	params := map[string]any{}
	if input.Action != "launch" {
		params["browserSessionId"] = input.BrowserSessionID
	}
	if input.PageID != "" {
		params["pageId"] = input.PageID
	}
	if input.TimeoutSeconds > 0 {
		params["timeoutMs"] = input.TimeoutSeconds * 1000
	}
	switch input.Action {
	case "launch":
		params["engine"] = input.Engine
		params["headless"] = !input.Headed
		if input.ViewportWidth > 0 || input.ViewportHeight > 0 {
			width, height := input.ViewportWidth, input.ViewportHeight
			if width == 0 {
				width = 1280
			}
			if height == 0 {
				height = 720
			}
			params["viewport"] = map[string]any{"width": width, "height": height}
		}
	case "page.open", "page.navigate":
		params["url"] = input.URL
		params["waitUntil"] = input.WaitUntil
	case "click", "type", "press", "wait":
		if input.Locator != nil {
			params["locator"] = map[string]any{
				"role": input.Locator.Role, "name": input.Locator.Name, "label": input.Locator.Label,
				"text": input.Locator.Text, "testId": input.Locator.TestID, "css": input.Locator.CSS, "exact": input.Locator.Exact,
			}
		}
		if input.Action == "type" {
			params["text"] = input.Text
		}
		if input.Action == "press" {
			params["key"] = input.Key
		}
		if input.Action == "wait" {
			params["state"] = input.State
		}
	case "screenshot":
		params["fullPage"] = input.FullPage
		params["format"] = input.Format
		if input.Quality > 0 {
			params["quality"] = input.Quality
		}
	case "events":
		params["cursor"] = input.Cursor
	}
	return params
}

func decodeCapabilityResult(input map[string]any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func toMCPMachine(machine core.MachineView) mcpMachine {
	out := mcpMachine{
		MachineID:            machine.MachineID,
		DisplayName:          machine.DisplayName,
		AdminNote:            machine.AdminNote,
		Status:               machine.Status,
		Online:               machine.Online,
		RuntimeStatus:        machine.RuntimeStatus,
		OS:                   machine.OS,
		Arch:                 machine.Arch,
		NodeVersion:          machine.NodeVersion,
		Generation:           machine.Generation,
		RegistrationMode:     machine.RegistrationMode,
		ConfigurationScope:   machine.ConfigurationScope,
		RuntimeCredential:    machine.RuntimeCredential,
		ConnectionTokenSaved: machine.ConnectionTokenSaved,
		Capabilities:         machine.Capabilities,
	}
	if machine.LastSeenAt != nil {
		out.LastSeenAt = protocolv1.Timestamp(*machine.LastSeenAt)
	}
	return out
}
