package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpContextKey string

const mcpOwnerKey mcpContextKey = "fast-spider-owner-id"

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
	JobID       string `json:"jobId" jsonschema:"opaque job ID returned by shell_run"`
	Cursor      int64  `json:"cursor,omitempty" jsonschema:"last consumed event sequence"`
	WaitSeconds int64  `json:"waitSeconds,omitempty" jsonschema:"long-poll wait from 0 to 15 seconds"`
}

type jobCancelInput struct {
	MachineID string `json:"machineId" jsonschema:"opaque Fast Spider machine ID"`
	JobID     string `json:"jobId" jsonschema:"opaque job ID returned by shell_run"`
}

type mcpMachine struct {
	MachineID     string                            `json:"machineId"`
	DisplayName   string                            `json:"displayName"`
	Status        string                            `json:"status"`
	Online        bool                              `json:"online"`
	RuntimeStatus string                            `json:"runtimeStatus,omitempty"`
	OS            string                            `json:"os"`
	Arch          string                            `json:"arch"`
	NodeVersion   string                            `json:"nodeVersion"`
	Generation    int64                             `json:"generation"`
	LastSeenAt    string                            `json:"lastSeenAt,omitempty"`
	Capabilities  []protocolv1.CapabilityDescriptor `json:"capabilities,omitempty"`
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
		ownerID, _ := r.Context().Value(mcpOwnerKey).(string)
		if ownerID == "" {
			return nil
		}
		return s.mcpServerFor(ownerID)
	}, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
		Stateless:    true,
	})

	limited := http.MaxBytesHandler(base, maxControlMessageBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, err := s.authenticateOwnerRequest(r)
		if err != nil {
			writeError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), mcpOwnerKey, ownerID)
		limited.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) mcpServerFor(ownerID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "fast-spider", Version: s.service.Version()}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "machine_list",
		Description: "List Fast Spider machines owned by the authenticated owner, including current online state and negotiated capabilities.",
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
		Name: "machine_get",
		Description: "Get one Fast Spider machine by opaque machineId. This never accepts a local filesystem path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input machineGetInput) (*mcp.CallToolResult, machineGetOutput, error) {
		machine, err := s.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, machineGetOutput{}, err
		}
		return nil, machineGetOutput{Machine: toMCPMachine(machine)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "capability_list",
		Description: "List the fixed Fast Spider capability catalog, or capabilities currently reported by a specific machine.",
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
		Name: "workspace_list",
		Description: "List Node-authorized workspaces by opaque workspaceId. Local absolute paths are never returned through this remote tool.",
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
		Name: "file_read",
		Description: "Read UTF-8 text from a relative path inside a Node-authorized workspace. Absolute paths and workspace escapes are rejected by the Node.",
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
		Name: "code_search",
		Description: "Search text files inside a Node-authorized workspace with bounded files, file sizes, matches and request deadline.",
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
		Name: "file_edit",
		Description: "Perform one exact optimistic-concurrency text replacement inside a Node-authorized workspace. Write permission must be enabled locally on the Node.",
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
		Name: "shell_run",
		Description: "Start a bounded non-interactive process in a Node-authorized workspace using an explicit argv array. Shell permission must be enabled locally on the Node.",
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
		Name: "job_watch",
		Description: "Read bounded stdout/stderr/status events for one Node job after a cursor, optionally long-polling for up to 15 seconds.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobWatchInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "", "job.control", "watch", map[string]any{"jobId": input.JobID, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds})
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
		Name: "job_cancel",
		Description: "Cancel one active Node job and terminate its process tree. Repeated cancellation of a terminal job is safe.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobCancelInput) (*mcp.CallToolResult, jobOutput, error) {
		result, err := s.service.CallCapability(ctx, ownerID, input.MachineID, "", "job.control", "cancel", map[string]any{"jobId": input.JobID})
		if err != nil {
			return nil, jobOutput{}, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, jobOutput{}, err
		}
		return nil, out, nil
	})

	return server
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
		MachineID: machine.MachineID,
		DisplayName: machine.DisplayName,
		Status: machine.Status,
		Online: machine.Online,
		RuntimeStatus: machine.RuntimeStatus,
		OS: machine.OS,
		Arch: machine.Arch,
		NodeVersion: machine.NodeVersion,
		Generation: machine.Generation,
		Capabilities: machine.Capabilities,
	}
	if machine.LastSeenAt != nil {
		out.LastSeenAt = protocolv1.Timestamp(*machine.LastSeenAt)
	}
	return out
}
