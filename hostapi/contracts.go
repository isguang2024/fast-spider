package hostapi

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AgentController is the stable cross-module boundary between the public Node
// host and an agent implementation. Implementations may live in another Go
// module; the public host must not require imports from that module.
type AgentController interface {
	Control(context.Context, string, map[string]any) (map[string]any, error)
	Close(context.Context) error
}

// AgentActionSource lets an external composition add specialized agent.control
// actions to the Node's advertised capability without changing the public
// baseline contract.
type AgentActionSource interface {
	AdditionalAgentActions() []string
}

// CapabilityError lets an injected agent return a bounded public capability
// error without exposing provider-private implementation details.
type CapabilityError interface {
	error
	CapabilityError() (code, message string, retryable bool)
}

// CloudResultPublisher is the Node-owned authenticated result publication hook.
type CloudResultPublisher interface {
	PublishCloudResult(context.Context, string, string, string) (map[string]any, error)
}

// NativeRunnerJobSpec is the stable subset of the public Node Job contract
// needed by a specialized runner. Job persistence and process ownership remain
// inside the public Node host.
type NativeRunnerJobSpec struct {
	Cwd            string
	Argv           []string
	Runtime        string
	Timeout        time.Duration
	IdempotencyKey string
}

// NativeRunnerJobSnapshot is a bounded view of a Node-owned Job.
type NativeRunnerJobSnapshot struct {
	JobID    string
	Runtime  string
	State    string
	ExitCode *int
	Error    string
	Evidence string
}

// NativeRunnerJobExecutor lets a specialized runner reuse the Node JobManager.
type NativeRunnerJobExecutor interface {
	Start(context.Context, NativeRunnerJobSpec) (NativeRunnerJobSnapshot, error)
	Watch(context.Context, string) (NativeRunnerJobSnapshot, error)
}

// CapabilityCaller exposes the in-process Node capability dispatcher through a
// small cross-module interface.
type CapabilityCaller interface {
	CallCapability(context.Context, string, string, map[string]any) (map[string]any, error)
}

// CapabilityCallFunc adapts a function to CapabilityCaller.
type CapabilityCallFunc func(context.Context, string, string, map[string]any) (map[string]any, error)

func (f CapabilityCallFunc) CallCapability(ctx context.Context, capability, action string, params map[string]any) (map[string]any, error) {
	return f(ctx, capability, action, params)
}

// HostBindings are Node-owned services injected into an external agent after
// the Node has initialized its identity, JobManager and capability dispatcher.
type HostBindings struct {
	ResultPublisher CloudResultPublisher
	JobExecutor     NativeRunnerJobExecutor
	MachineID       func() (string, error)
	Capabilities    CapabilityCaller
}

// AgentHostBinder is optional. An injected agent implements it when it needs
// Node-owned services such as Jobs, FS capability calls or result publication.
type AgentHostBinder interface {
	BindHost(HostBindings)
}

// UISurfaceContext contains only host facilities that a specialized local UI
// surface needs. APIOnly applies the public Node UI token/origin guard.
type UISurfaceContext struct {
	DataDir string
	Version string
	UIToken string
	APIOnly func(http.HandlerFunc) http.HandlerFunc
}

// UISurface registers optional specialized routes into the single local Node UI
// HTTP server.
type UISurface interface {
	RegisterUI(*http.ServeMux, UISurfaceContext) error
}

// UISurfaceFunc adapts a function to UISurface.
type UISurfaceFunc func(*http.ServeMux, UISurfaceContext) error

func (f UISurfaceFunc) RegisterUI(mux *http.ServeMux, ctx UISurfaceContext) error {
	return f(mux, ctx)
}

// MCPSurfaceContext exposes bounded host context to optional specialized MCP
// tools without granting access to internal packages.
type MCPSurfaceContext struct {
	DataDir      string
	Version      string
	Capabilities CapabilityCaller
}

// MCPSurface registers optional specialized tools into the existing local MCP
// server. The public server remains the transport owner.
type MCPSurface interface {
	RegisterMCP(*mcp.Server, MCPSurfaceContext) error
}

// MCPSurfaceFunc adapts a function to MCPSurface.
type MCPSurfaceFunc func(*mcp.Server, MCPSurfaceContext) error

func (f MCPSurfaceFunc) RegisterMCP(server *mcp.Server, ctx MCPSurfaceContext) error {
	return f(server, ctx)
}
