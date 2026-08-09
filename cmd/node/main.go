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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent"
	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/nodeinstance"
	"github.com/isguang2024/fast-spider/internal/nodeui"
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
	case "workspace-add":
		runWorkspaceAdd(os.Args[2:])
	case "workspace-list":
		runWorkspaceList(os.Args[2:])
	case "workspace-enable":
		runWorkspaceEnabled(os.Args[2:], true)
	case "workspace-disable":
		runWorkspaceEnabled(os.Args[2:], false)
	case "workspace-remove":
		runWorkspaceRemove(os.Args[2:])
	case "workspace-permission":
		runWorkspacePermission(os.Args[2:])
	case "workspace-profile-set":
		runWorkspaceProfileSet(os.Args[2:])
	case "workspace-profile-list":
		runWorkspaceProfileList(os.Args[2:])
	case "workspace-profile-remove":
		runWorkspaceProfileRemove(os.Args[2:])
	case "workspace-browser-allow":
		runWorkspaceBrowserAllow(os.Args[2:])
	case "workspace-browser-list":
		runWorkspaceBrowserList(os.Args[2:])
	case "workspace-browser-remove":
		runWorkspaceBrowserRemove(os.Args[2:])
	case "local-call":
		runLocalCall(os.Args[2:])
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
	_ = fs.Parse(args)
	app, err := nodeui.New(nodeui.Options{DataDir: *dataDir, Version: version.Version, MachineName: hostname(), Logger: logger})
	fatalIf(err)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalIf(app.Run(ctx))
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
	_ = fs.Parse(args)
	if *hubURL == "" || *token == "" {
		fatalIf(errors.New("connect requires --hub and --token"))
	}
	lease, err := nodeinstance.Acquire(*dataDir)
	fatalIf(err)
	defer lease.Close()
	client, err := node.New(node.Config{
		DataDir: *dataDir, Version: version.Version, AllowInsecure: *allowInsecure, Logger: logger,
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
	})
}

type nodeRunOptions struct {
	dataDir            string
	allowInsecure      bool
	browserSidecarDir  string
	disableLocalBridge bool
}

func runNode(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	allowInsecure := fs.Bool("allow-insecure", false, "allow a previously registered http/ws Hub only for local development")
	browserSidecarDir := fs.String("browser-sidecar-dir", "", "optional Playwright sidecar directory; defaults to FAST_SPIDER_BROWSER_SIDECAR_DIR or ./sidecar/browser")
	disableLocalBridge := fs.Bool("disable-local-bridge", false, "disable the current-user AF_UNIX Local Bridge")
	_ = fs.Parse(args)
	lease, err := nodeinstance.Acquire(*dataDir)
	fatalIf(err)
	defer lease.Close()
	runNodeLocked(logger, nodeRunOptions{
		dataDir:            *dataDir,
		allowInsecure:      *allowInsecure,
		browserSidecarDir:  *browserSidecarDir,
		disableLocalBridge: *disableLocalBridge,
	})
}

func runNodeLocked(logger *slog.Logger, opts nodeRunOptions) {
	agentController := agent.New(opts.dataDir, logger)
	client, err := node.New(node.Config{DataDir: opts.dataDir, Version: version.Version, AllowInsecure: opts.allowInsecure, BrowserSidecarDir: opts.browserSidecarDir, Agent: agentController, Logger: logger})
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

func runWorkspaceAdd(args []string) {
	fs := flag.NewFlagSet("workspace-add", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	path := fs.String("path", "", "local directory to authorize")
	name := fs.String("name", "", "workspace display name")
	_ = fs.Parse(args)
	if *path == "" {
		fatalIf(errors.New("--path is required"))
	}
	record, err := node.NewWorkspaceStore(*dataDir).Add(*path, *name)
	fatalIf(err)
	printJSON(record)
}

func runWorkspaceList(args []string) {
	fs := flag.NewFlagSet("workspace-list", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	_ = fs.Parse(args)
	items, err := node.NewWorkspaceStore(*dataDir).ListLocal()
	fatalIf(err)
	printJSON(map[string]any{"workspaces": items})
}

func runWorkspaceEnabled(args []string, enabled bool) {
	fs := flag.NewFlagSet("workspace-state", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	_ = fs.Parse(args)
	if *workspaceID == "" {
		fatalIf(errors.New("--workspace is required"))
	}
	fatalIf(node.NewWorkspaceStore(*dataDir).SetEnabled(*workspaceID, enabled))
	printJSON(map[string]any{"workspaceId": *workspaceID, "enabled": enabled})
}

func runWorkspacePermission(args []string) {
	fs := flag.NewFlagSet("workspace-permission", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	allow := fs.String("allow", "read", "comma-separated permissions: read,write,shell,git-write,git-network,git-hooks,build; read is always retained")
	_ = fs.Parse(args)
	if *workspaceID == "" {
		fatalIf(errors.New("--workspace is required"))
	}
	permissions := strings.Split(*allow, ",")
	store := node.NewWorkspaceStore(*dataDir)
	fatalIf(store.SetPermissions(*workspaceID, permissions))
	items, err := store.List()
	fatalIf(err)
	for _, item := range items {
		if item.WorkspaceId == *workspaceID {
			printJSON(map[string]any{"workspaceId": *workspaceID, "permissions": item.Permissions})
			return
		}
	}
	fatalIf(node.ErrWorkspaceNotFound)
}

func runWorkspaceProfileSet(args []string) {
	fs := flag.NewFlagSet("workspace-profile-set", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	profileID := fs.String("profile", "", "build profile ID")
	name := fs.String("name", "", "build profile display name")
	argvJSON := fs.String("argv-json", "", "JSON array containing the fixed local argv")
	cwd := fs.String("cwd", ".", "relative working directory inside the workspace")
	timeoutSeconds := fs.Int64("timeout-seconds", 600, "profile timeout in seconds, maximum 1800")
	_ = fs.Parse(args)
	if *workspaceID == "" || *profileID == "" || *argvJSON == "" {
		fatalIf(errors.New("--workspace, --profile and --argv-json are required"))
	}
	var argv []string
	fatalIf(json.Unmarshal([]byte(*argvJSON), &argv))
	profile := node.BuildProfileRecord{ProfileID: *profileID, DisplayName: *name, Argv: argv, Cwd: *cwd, TimeoutSeconds: *timeoutSeconds}
	fatalIf(node.NewWorkspaceStore(*dataDir).SetBuildProfile(*workspaceID, profile))
	printJSON(map[string]any{"workspaceId": *workspaceID, "profileId": *profileID, "saved": true})
}

func runWorkspaceProfileList(args []string) {
	fs := flag.NewFlagSet("workspace-profile-list", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	_ = fs.Parse(args)
	if *workspaceID == "" {
		fatalIf(errors.New("--workspace is required"))
	}
	profiles, err := node.NewWorkspaceStore(*dataDir).BuildProfiles(*workspaceID)
	fatalIf(err)
	printJSON(map[string]any{"workspaceId": *workspaceID, "profiles": profiles})
}

func runWorkspaceProfileRemove(args []string) {
	fs := flag.NewFlagSet("workspace-profile-remove", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	profileID := fs.String("profile", "", "build profile ID")
	_ = fs.Parse(args)
	if *workspaceID == "" || *profileID == "" {
		fatalIf(errors.New("--workspace and --profile are required"))
	}
	fatalIf(node.NewWorkspaceStore(*dataDir).RemoveBuildProfile(*workspaceID, *profileID))
	printJSON(map[string]any{"workspaceId": *workspaceID, "profileId": *profileID, "removed": true})
}

func runWorkspaceBrowserAllow(args []string) {
	fs := flag.NewFlagSet("workspace-browser-allow", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	origin := fs.String("origin", "", "persistent local/private http(s) origin to allow, including port when non-default")
	_ = fs.Parse(args)
	if *workspaceID == "" || *origin == "" {
		fatalIf(errors.New("--workspace and --origin are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := node.NewWorkspaceStore(*dataDir).AuthorizeBrowserOrigin(ctx, *workspaceID, *origin)
	fatalIf(err)
	printJSON(record)
}

func runWorkspaceBrowserList(args []string) {
	fs := flag.NewFlagSet("workspace-browser-list", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	_ = fs.Parse(args)
	if *workspaceID == "" {
		fatalIf(errors.New("--workspace is required"))
	}
	record, err := node.NewWorkspaceStore(*dataDir).Resolve(*workspaceID)
	fatalIf(err)
	printJSON(map[string]any{"workspaceId": *workspaceID, "origins": record.BrowserOrigins})
}

func runWorkspaceBrowserRemove(args []string) {
	fs := flag.NewFlagSet("workspace-browser-remove", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	origin := fs.String("origin", "", "exact http(s) origin to revoke")
	_ = fs.Parse(args)
	if *workspaceID == "" || *origin == "" {
		fatalIf(errors.New("--workspace and --origin are required"))
	}
	fatalIf(node.NewWorkspaceStore(*dataDir).RevokeBrowserOrigin(*workspaceID, *origin))
	printJSON(map[string]any{"workspaceId": *workspaceID, "origin": *origin, "removed": true})
}

func runLocalCall(args []string) {
	fs := flag.NewFlagSet("local-call", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId; omit only for capabilities that do not require one")
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
		WorkspaceId: *workspaceID,
		Capability:  *capability,
		Action:      *action,
		Params:      params,
	})
	fatalIf(err)
	printJSON(response)
	if response.Error != nil {
		os.Exit(1)
	}
}

func runWorkspaceRemove(args []string) {
	fs := flag.NewFlagSet("workspace-remove", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	workspaceID := fs.String("workspace", "", "opaque workspaceId")
	_ = fs.Parse(args)
	if *workspaceID == "" {
		fatalIf(errors.New("--workspace is required"))
	}
	fatalIf(node.NewWorkspaceStore(*dataDir).Remove(*workspaceID))
	printJSON(map[string]any{"workspaceId": *workspaceID, "removed": true})
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	fatalIf(encoder.Encode(value))
}

func defaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ".fast-spider-node"
	}
	return filepath.Join(base, "FastSpider", "node")
}

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
	fmt.Fprintln(os.Stderr, "usage: fast-spider-node <ui|connect|run|status|local-call|workspace-add|workspace-list|workspace-enable|workspace-disable|workspace-permission|workspace-profile-set|workspace-profile-list|workspace-profile-remove|workspace-browser-allow|workspace-browser-list|workspace-browser-remove|workspace-remove|version> [flags]")
}
