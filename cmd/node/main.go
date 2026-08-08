package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch os.Args[1] {
	case "enroll":
		runEnroll(logger, os.Args[2:])
	case "run":
		runNode(logger, os.Args[2:])
	case "status":
		runStatus(logger, os.Args[2:])
	case "version":
		fmt.Println(version.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func runEnroll(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	hubURL := fs.String("hub", "", "Hub base URL, normally https://host")
	token := fs.String("token", "", "one-time enrollment token")
	name := fs.String("name", hostname(), "machine display name")
	allowInsecure := fs.Bool("allow-insecure", false, "allow local-development http/ws Hub URLs")
	_ = fs.Parse(args)
	if *hubURL == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "enroll requires --hub and --token")
		os.Exit(2)
	}
	client, err := node.New(node.Config{DataDir: *dataDir, Version: version.Version, AllowInsecure: *allowInsecure, Logger: logger})
	fatalIf(err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state, err := client.Enroll(ctx, *hubURL, *token, *name)
	fatalIf(err)
	fmt.Printf("enrolled machineId=%s hubFingerprint=%s\n", state.MachineID, state.HubFingerprint)
}

func runNode(logger *slog.Logger, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "Node data directory")
	allowInsecure := fs.Bool("allow-insecure", false, "allow a previously enrolled http/ws Hub only for local development")
	_ = fs.Parse(args)
	client, err := node.New(node.Config{DataDir: *dataDir, Version: version.Version, AllowInsecure: *allowInsecure, Logger: logger})
	fatalIf(err)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = client.Run(ctx)
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
	fmt.Printf("machineId=%s\nhub=%s\nhubFingerprint=%s\n", state.MachineID, state.HubURL, state.HubFingerprint)
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
	fmt.Fprintln(os.Stderr, "usage: fast-spider-node <enroll|run|status|version> [flags]")
}
