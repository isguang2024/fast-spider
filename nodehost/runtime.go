// Package nodehost exposes the supported composition boundary for alternate
// Fast Spider Node builds. External modules import this package and hostapi;
// they never import packages under internal/.
package nodehost

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/isguang2024/fast-spider/hostapi"
	"github.com/isguang2024/fast-spider/internal/agent"
	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/localmcp"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/nodeui"
	"github.com/isguang2024/fast-spider/internal/operationlog"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

// NewDefaultAgent returns the public Node's built-in local provider controller.
// A specialized module can wrap this controller and delegate all non-private
// routes to it, preserving the public Codex/Claude behavior.
func NewDefaultAgent(dataDir string, logger *slog.Logger) hostapi.AgentController {
	return agent.New(dataDir, logger)
}

// State is the non-secret public Node identity view.
type State struct {
	HubURL         string
	MachineID      string
	HubFingerprint string
}

// RuntimeOptions configure the headless Node host. Agent may be an external
// composite controller; nil intentionally means no agent capability provider.
type RuntimeOptions struct {
	DataDir            string
	Version            string
	ProjectRoot        string
	BrowserSidecarDir  string
	AllowInsecure      bool
	DisableLocalBridge bool
	Agent              hostapi.AgentController
	AgentCallerOwned   bool
	Logger             *slog.Logger
}

// Runtime owns one public Node client and, unless disabled, one current-user
// Local Bridge. It does not create provider-specific sessions itself.
type Runtime struct {
	client             *node.Client
	dataDir            string
	disableLocalBridge bool
	logger             *slog.Logger
}

// NewRuntime constructs a Node runtime using the public Job, FS, browser,
// identity and result-publication implementations.
func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opLog, err := operationlog.NewStore(opts.DataDir, logger)
	if err != nil {
		logger.Warn("operation log store unavailable", "error", err)
	}
	client, err := node.New(node.Config{
		DataDir:           opts.DataDir,
		Version:           opts.Version,
		AllowInsecure:     opts.AllowInsecure,
		ProjectRoot:       opts.ProjectRoot,
		BrowserSidecarDir: opts.BrowserSidecarDir,
		Agent:             opts.Agent,
		AgentCallerOwned:  opts.AgentCallerOwned,
		Logger:            logger,
		OperationLog:      opLog,
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{client: client, dataDir: opts.DataDir, disableLocalBridge: opts.DisableLocalBridge, logger: logger}, nil
}

// Run keeps Node transport and the Local Bridge in the same process. The Node
// client remains the lifecycle owner for browser/agent shutdown semantics.
func (r *Runtime) Run(ctx context.Context) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("node runtime is nil")
	}
	if !r.disableLocalBridge {
		go func() {
			if err := localbridge.Run(ctx, r.dataDir, r.client.HandleLocalCapability); err != nil && ctx.Err() == nil {
				r.logger.Error("local bridge stopped", "endpoint", localbridge.Endpoint(r.dataDir), "error", err)
			}
		}()
	}
	return r.client.Run(ctx)
}

// Connect registers this Node with a Hub and returns a non-secret state view.
func (r *Runtime) Connect(ctx context.Context, hubURL, token, displayName string) (State, error) {
	if r == nil || r.client == nil {
		return State{}, fmt.Errorf("node runtime is nil")
	}
	state, err := r.client.Connect(ctx, hubURL, token, displayName)
	if err != nil {
		return State{}, err
	}
	return publicState(state), nil
}

// State returns the persisted non-secret Node identity.
func (r *Runtime) State() (State, error) {
	if r == nil || r.client == nil {
		return State{}, fmt.Errorf("node runtime is nil")
	}
	state, err := r.client.State()
	if err != nil {
		return State{}, err
	}
	return publicState(state), nil
}

// CallCapability invokes the same in-process dispatcher used by Local Bridge.
// This is the public escape hatch for a specialized module that needs generic
// FS/Job/browser capabilities without importing protocol or Node internals.
func (r *Runtime) CallCapability(ctx context.Context, capability, action string, params map[string]any) (map[string]any, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("node runtime is nil")
	}
	response := r.client.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{Capability: capability, Action: action, Params: params})
	if response.Error != nil {
		return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

func publicState(state node.State) State {
	return State{HubURL: state.HubURL, MachineID: state.MachineID, HubFingerprint: state.HubFingerprint}
}

// UIOptions configure the existing local Node UI with optional external agent
// and route surfaces. Nil Agent preserves the current public built-in behavior.
type UIOptions struct {
	DataDir          string
	Version          string
	MachineName      string
	NoOpenWindow     bool
	Logger           *slog.Logger
	Agent            hostapi.AgentController
	AgentCallerOwned bool
	Surfaces         []hostapi.UISurface
}

// UI is the supported wrapper around the public Node UI host.
type UI struct{ app *nodeui.App }

func NewUI(opts UIOptions) (*UI, error) {
	app, err := nodeui.New(nodeui.Options{
		DataDir:          opts.DataDir,
		Version:          opts.Version,
		MachineName:      opts.MachineName,
		NoOpenWindow:     opts.NoOpenWindow,
		Logger:           opts.Logger,
		Agent:            opts.Agent,
		AgentCallerOwned: opts.AgentCallerOwned,
		UISurfaces:       opts.Surfaces,
	})
	if err != nil {
		return nil, err
	}
	return &UI{app: app}, nil
}

func (u *UI) Run(ctx context.Context) error {
	if u == nil || u.app == nil {
		return fmt.Errorf("node UI is nil")
	}
	return u.app.Run(ctx)
}

// RunLocalMCP serves the existing current-user MCP transport and registers any
// external specialized tools into the same server instance.
func RunLocalMCP(ctx context.Context, dataDir, version string, logger *slog.Logger, surfaces ...hostapi.MCPSurface) error {
	return localmcp.RunWithSurfaces(ctx, dataDir, version, logger, surfaces)
}

// RunLocalMCPWithAgent advertises any specialized agent actions supplied by an
// external composition while preserving the same Local Bridge transport.
func RunLocalMCPWithAgent(ctx context.Context, dataDir, version string, logger *slog.Logger, agent hostapi.AgentController, surfaces ...hostapi.MCPSurface) error {
	return localmcp.RunWithAgentAndSurfaces(ctx, dataDir, version, logger, agent, surfaces)
}
