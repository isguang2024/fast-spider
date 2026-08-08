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
	Limit       int64  `json:"limit,omitempty" jsonschema:"maximum bytes to return, default and maximum 1048576"`
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
	Encoding    string `json:"encoding"`
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
