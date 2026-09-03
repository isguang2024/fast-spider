package localmcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverInstructions = `FastSpider_Local connects Codex directly to the Fast Spider Node running as the same OS user. It uses the current-user Local Bridge and never routes capability calls through the Hub.

Call local_machine first when the local Node identity or capability catalog is needed. Use local_capability with one advertised capability/action and its normal Node parameters. Local transport does not make an underlying network-dependent action offline: remote Git, browser navigation, cloud AI, artifact publication, and provider authentication may still require network access.

Mutations keep their existing Node contracts. Preserve idempotency keys, use file read/SHA/preview/CAS for edits, drive every started job to a terminal state with job.control/watch, close caller-owned browser sessions, and do not retry an uncertain external create with a new key.`

type emptyInput struct{}

type machineOutput struct {
	Transport      string                            `json:"transport"`
	Registered     bool                              `json:"registered"`
	MachineID      string                            `json:"machineId,omitempty"`
	BridgeEndpoint string                            `json:"bridgeEndpoint"`
	Capabilities   []protocolv1.CapabilityDescriptor `json:"capabilities"`
}

type capabilityCallInput struct {
	Capability     string         `json:"capability" jsonschema:"advertised Node capability ID such as file.read, code.search, shell.exec, git.repository, browser.automation, agent.control, or working.context"`
	Action         string         `json:"action" jsonschema:"action advertised for the selected capability"`
	Params         map[string]any `json:"params,omitempty" jsonschema:"capability-specific parameters; local routing does not require machineId"`
	TimeoutSeconds int64          `json:"timeoutSeconds,omitempty" jsonschema:"local call timeout in seconds; default 60 and maximum 600"`
}

type capabilityCallOutput struct {
	RequestID string         `json:"requestId"`
	TraceID   string         `json:"traceId,omitempty"`
	Result    map[string]any `json:"result"`
}

type bridgeCaller func(context.Context, string, protocolv1.CapabilityRequest) (protocolv1.CapabilityResponse, error)

// New returns an MCP server that translates STDIO MCP calls to the existing
// current-user Local Bridge. It does not create another Node or capability
// engine and does not connect to the Hub.
func New(dataDir, version string, logger *slog.Logger) *mcp.Server {
	return newServer(dataDir, version, logger, localbridge.Call)
}

func newServer(dataDir, version string, logger *slog.Logger, call bridgeCaller) *mcp.Server {
	if logger == nil {
		logger = slog.Default()
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "fast-spider-local", Title: "FastSpider_Local", Version: version},
		&mcp.ServerOptions{Instructions: serverInstructions, Logger: logger},
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "local_machine",
		Description: "Read the co-located Fast Spider Node identity and capability catalog. This is local discovery and does not contact the Hub.",
		Annotations: toolAnnotations(true, false, true, false),
	}, func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, machineOutput, error) {
		state, err := node.LoadState(filepath.Join(dataDir, "state.json"))
		registered := err == nil
		if err != nil && !errors.Is(err, node.ErrNotRegistered) {
			return nil, machineOutput{}, err
		}
		capabilities := append([]protocolv1.CapabilityDescriptor(nil), protocolv1.NodeCapabilities...)
		capabilities = append(capabilities, protocolv1.ScreenshotCapabilityForOS(runtime.GOOS), protocolv1.BrowserCapability)
		return emptyResult(), machineOutput{
			Transport:      "local",
			Registered:     registered,
			MachineID:      state.MachineID,
			BridgeEndpoint: localbridge.Endpoint(dataDir),
			Capabilities:   capabilities,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "local_capability",
		Description: "Call one capability on the co-located Fast Spider Node through its current-user Local Bridge. No machineId or Hub connection is used. The selected capability may itself require network access.",
		Annotations: toolAnnotations(false, true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input capabilityCallInput) (*mcp.CallToolResult, capabilityCallOutput, error) {
		capability := strings.TrimSpace(input.Capability)
		action := strings.TrimSpace(input.Action)
		if capability == "" || action == "" {
			return nil, capabilityCallOutput{}, fmt.Errorf("capability and action are required")
		}
		timeout := input.TimeoutSeconds
		if timeout == 0 {
			timeout = 60
		}
		if timeout < 1 || timeout > 600 {
			return nil, capabilityCallOutput{}, fmt.Errorf("timeoutSeconds must be between 1 and 600")
		}
		params := input.Params
		if params == nil {
			params = map[string]any{}
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		response, err := call(callCtx, dataDir, protocolv1.CapabilityRequest{
			Capability: capability,
			Action:     action,
			Params:     params,
			Deadline:   protocolv1.Timestamp(time.Now().UTC().Add(time.Duration(timeout) * time.Second)),
		})
		if err != nil {
			return nil, capabilityCallOutput{}, fmt.Errorf("local bridge call failed: %w", err)
		}
		if response.Error != nil {
			return nil, capabilityCallOutput{}, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
		}
		return emptyResult(), capabilityCallOutput{RequestID: response.RequestId, TraceID: response.TraceId, Result: response.Result}, nil
	})

	return server
}

// Run serves the local MCP connection over stdin/stdout. All logs must use
// stderr so stdout remains a valid MCP JSONL stream.
func Run(ctx context.Context, dataDir, version string, logger *slog.Logger) error {
	return New(dataDir, version, logger).Run(ctx, &mcp.StdioTransport{})
}

func emptyResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{}}
}

func toolAnnotations(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}
