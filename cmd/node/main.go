package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent"
	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/localmcp"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/nodeinstance"
	"github.com/isguang2024/fast-spider/internal/nodeui"
	"github.com/isguang2024/fast-spider/internal/nodeupdate"
	"github.com/isguang2024/fast-spider/internal/operationlog"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) < 2 {
		launchDefaultUI(logger)
		return
	}

	switch os.Args[1] {
	case "ui":
		runUI(logger, os.Args[2:])
	case "connect":
		runConnect(logger, os.Args[2:])
	case "run":
		runNode(logger, os.Args[2:])
	case "status":
		runStatus(logger, os.Args[2:])
	case "self-update-apply":
		runSelfUpdateApply(os.Args[2:])
	case "local-call":
		runLocalCall(os.Args[2:])
	case "mcp-local":
		runLocalMCP(os.Args[2:])
	case "version":
		fmt.Println(version.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func runUI(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	background := fs.Bool("background", false, "start Node without opening the local UI window")
	_ = fs.Parse(args)
	if *background || os.Getenv("FAST_SPIDER_HIDDEN_UI") == "1" {
		hideConsoleWindow()
	}
	app, err := nodeui.New(nodeui.Options{DataDir: *dataDir, Version: version.Version, MachineName: hostname(), NoOpenWindow: *background, Logger: logger})
	fatalIf(err)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalIf(app.Run(ctx))
}

func runSelfUpdateApply(args []string) {
	fs := flag.NewFlagSet("self-update-apply", flag.ExitOnError)
	target := fs.String("target", "", "executable path to replace")
	pid := fs.Int("pid", 0, "old process id")
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	background := fs.Bool("background", false, "restart without opening the local UI window")
	_ = fs.Parse(args)
	fatalIf(nodeupdate.ApplyHelper(*target, *dataDir, *pid, *background))
}

func runConnect(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	hubURL := fs.String("hub", "", "Hub base URL, normally https://host")
	token := fs.String("token", "", "connection token created in the Hub Web Console")
	name := fs.String("name", hostname(), "machine display name")
	allowInsecure := fs.Bool("allow-insecure", false, "allow local-development http/ws Hub URLs")
	noRun := fs.Bool("no-run", false, "finish after registration without starting the Node connection")
	browserSidecarDir := fs.String("browser-sidecar-dir", "", "optional Playwright sidecar directory")
	disableLocalBridge := fs.Bool("disable-local-bridge", false, "disable the current-user AF_UNIX Local Bridge after connect")
	projectRoot := fs.String("project-root", "", "project root for safe Project mode; omit to keep Machine mode")
	_ = fs.Parse(args)
	if *hubURL == "" || *token == "" {
		fatalIf(errors.New("connect requires --hub and --token"))
	}
	lease, err := nodeinstance.Acquire()
	fatalIf(err)
	defer lease.Close()
	client, err := node.New(node.Config{
		DataDir: *dataDir, Version: version.Version, AllowInsecure: *allowInsecure, ProjectRoot: *projectRoot, Logger: logger,
	})
	fatalIf(err)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state, err := client.Connect(ctx, *hubURL, *token, *name)
	fatalIf(err)
	fmt.Printf("connected machineId=%s hubFingerprint=%s\n", state.MachineID, state.HubFingerprint)
	if *noRun {
		return
	}
	runNodeLocked(logger, nodeRunOptions{
		dataDir:            *dataDir,
		allowInsecure:      *allowInsecure,
		browserSidecarDir:  *browserSidecarDir,
		disableLocalBridge: *disableLocalBridge,
		projectRoot:        *projectRoot,
	})
}

type nodeRunOptions struct {
	dataDir            string
	allowInsecure      bool
	browserSidecarDir  string
	disableLocalBridge bool
	projectRoot        string
}

func runNode(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	allowInsecure := fs.Bool("allow-insecure", false, "allow a previously registered http/ws Hub only for local development")
	browserSidecarDir := fs.String("browser-sidecar-dir", "", "optional Playwright sidecar directory; defaults to FAST_SPIDER_BROWSER_SIDECAR_DIR or ./sidecar/browser")
	disableLocalBridge := fs.Bool("disable-local-bridge", false, "disable the current-user AF_UNIX Local Bridge")
	projectRoot := fs.String("project-root", "", "project root for safe Project mode; omit to keep Machine mode")
	_ = fs.Parse(args)
	lease, err := nodeinstance.Acquire()
	fatalIf(err)
	defer lease.Close()
	runNodeLocked(logger, nodeRunOptions{
		dataDir:            *dataDir,
		allowInsecure:      *allowInsecure,
		browserSidecarDir:  *browserSidecarDir,
		disableLocalBridge: *disableLocalBridge,
		projectRoot:        *projectRoot,
	})
}

func runNodeLocked(logger *slog.Logger, opts nodeRunOptions) {
	agentController := agent.New(opts.dataDir, logger)
	operationLog, logErr := operationlog.NewStore(opts.dataDir, logger)
	if logErr != nil {
		logger.Warn("operation log store unavailable", "error", logErr)
	}
	client, err := node.New(node.Config{DataDir: opts.dataDir, Version: version.Version, AllowInsecure: opts.allowInsecure, ProjectRoot: opts.projectRoot, BrowserSidecarDir: opts.browserSidecarDir, Agent: agentController, Logger: logger, OperationLog: operationLog})
	fatalIf(err)
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	if !opts.disableLocalBridge {
		logger.Info("local bridge enabled", "endpoint", localbridge.Endpoint(opts.dataDir))
		go func() {
			if bridgeErr := localbridge.Run(ctx, opts.dataDir, client.HandleLocalCapability); bridgeErr != nil && ctx.Err() == nil {
				logger.Error("local bridge stopped", "endpoint", localbridge.Endpoint(opts.dataDir), "error", bridgeErr)
			}
		}()
	}
	err = client.Run(ctx)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		fatalIf(err)
	}
}

func runStatus(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	_ = fs.Parse(args)
	client, err := node.New(node.Config{DataDir: *dataDir, Version: version.Version, Logger: logger})
	fatalIf(err)
	state, err := client.State()
	fatalIf(err)
	fmt.Printf("machineId=%s\nhub=%s\nhubFingerprint=%s\nlocalBridge=%s\n", state.MachineID, state.HubURL, state.HubFingerprint, localbridge.Endpoint(*dataDir))
}

func runLocalCall(args []string) {
	fs := flag.NewFlagSet("local-call", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	capability := fs.String("capability", "", "capability ID such as file.read, code.search, shell.exec")
	action := fs.String("action", "", "capability action")
	paramsJSON := fs.String("params-json", "{}", "JSON object containing capability parameters")
	timeoutSeconds := fs.Int64("timeout-seconds", 60, "local call timeout in seconds, maximum 600")
	_ = fs.Parse(args)
	if *capability == "" || *action == "" {
		fatalIf(errors.New("--capability and --action are required"))
	}
	if *timeoutSeconds < 1 || *timeoutSeconds > 600 {
		fatalIf(errors.New("--timeout-seconds must be between 1 and 600"))
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(*paramsJSON), &params); err != nil {
		fatalIf(fmt.Errorf("invalid --params-json: %w", err))
	}
	if params == nil {
		params = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds)*time.Second)
	defer cancel()
	response, err := localbridge.Call(ctx, *dataDir, protocolv1.CapabilityRequest{
		Capability: *capability,
		Action:     *action,
		Params:     params,
	})
	fatalIf(err)
	printJSON(response)
	if response.Error != nil {
		os.Exit(1)
	}
}

func runLocalMCP(args []string) {
	fs := flag.NewFlagSet("mcp-local", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	_ = fs.Parse(args)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalIf(localmcp.Run(ctx, *dataDir, version.Version, logger))
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	fatalIf(encoder.Encode(value))
}

func defaultDataDir() string { return platformDefaultDataDir() }

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "fast-spider-node"
	}
	return name
}

func fatalIf(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fast-spider-node <ui|connect|run|status|local-call|mcp-local|version> [flags]")
}
