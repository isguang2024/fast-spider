package server

import (
	"context"
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
	MachineID string `json:"machineId,omitempty" jsonschema:"optional machine ID; omit for the Phase 1 Hub capability catalog"`
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
		Description: "List the fixed Phase 1 capability catalog, or capabilities currently reported by a specific machine.",
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

	return server
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
