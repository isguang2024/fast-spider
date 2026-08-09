package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/adminclient"
	"github.com/isguang2024/fast-spider/internal/opsbackup"
	"github.com/isguang2024/fast-spider/internal/version"
)

type commonFlags struct {
	hub            *string
	allowInsecure  *bool
	ownerTokenFile *string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	command := os.Args[1]
	switch command {
	case "setup-url":
		setupURL(os.Args[2:])
		return
	case "backup":
		backup(os.Args[2:])
		return
	case "backup-verify":
		backupVerify(os.Args[2:])
		return
	case "restore":
		restore(os.Args[2:])
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch command {
	case "bootstrap":
		bootstrap(ctx, os.Args[2:])
	case "enrollment-create":
		createEnrollment(ctx, os.Args[2:])
	case "machine-list":
		machineList(ctx, os.Args[2:])
	case "machine-get":
		machineGet(ctx, os.Args[2:])
	case "machine-revoke":
		machineRevoke(ctx, os.Args[2:])
	case "version":
		fmt.Println(version.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func setupURL(args []string) {
	fs := flag.NewFlagSet("setup-url", flag.ExitOnError)
	publicURL := fs.String("public-url", "", "public Hub base URL, for example https://host/fast-spider")
	bootstrapTokenFile := fs.String("bootstrap-token-file", "", "path to Hub bootstrap-token file")
	allowInsecure := fs.Bool("allow-insecure", false, "allow an http setup URL only for local development")
	_ = fs.Parse(args)
	if *publicURL == "" || *bootstrapTokenFile == "" {
		fatalf("--public-url and --bootstrap-token-file are required")
	}
	base, err := url.Parse(strings.TrimSpace(*publicURL))
	fatalIf(err)
	if (base.Scheme != "https" && !(*allowInsecure && base.Scheme == "http")) || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		fatalf("--public-url must be an https base URL without credentials, query or fragment; http requires --allow-insecure")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/setup"
	base.Fragment = "code=" + readSecret(*bootstrapTokenFile)
	fmt.Println(base.String())
	fmt.Fprintln(os.Stderr, "This one-time setup URL contains the bootstrap secret. Open it directly and do not store or share it.")
}

func bootstrap(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	common := addCommon(fs)
	bootstrapTokenFile := fs.String("bootstrap-token-file", "", "path to Hub bootstrap-token file")
	displayName := fs.String("name", "Owner", "Owner display name")
	_ = fs.Parse(args)
	if *bootstrapTokenFile == "" {
		fatalf("--bootstrap-token-file is required")
	}
	token := readSecret(*bootstrapTokenFile)
	client := newClient(common, "")
	result, err := client.Bootstrap(ctx, token, *displayName)
	fatalIf(err)
	printJSON(result)
	fmt.Fprintln(os.Stderr, "Owner token is returned once. Store it in a protected secret store or FAST_SPIDER_OWNER_TOKEN; it is not recoverable from Hub storage.")
}

func createEnrollment(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("enrollment-create", flag.ExitOnError)
	common := addCommon(fs)
	expectedName := fs.String("name", "", "optional expected Node display name")
	expectedOS := fs.String("os", "", "optional expected Node OS, for example windows or linux")
	_ = fs.Parse(args)
	client := newClient(common, ownerToken(*common.ownerTokenFile))
	result, err := client.CreateEnrollment(ctx, *expectedName, *expectedOS)
	fatalIf(err)
	printJSON(result)
}

func machineList(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("machine-list", flag.ExitOnError)
	common := addCommon(fs)
	_ = fs.Parse(args)
	client := newClient(common, ownerToken(*common.ownerTokenFile))
	machines, err := client.ListMachines(ctx)
	fatalIf(err)
	printJSON(map[string]any{"machines": machines})
}

func machineGet(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("machine-get", flag.ExitOnError)
	common := addCommon(fs)
	machineID := fs.String("machine", "", "opaque machineId")
	_ = fs.Parse(args)
	if *machineID == "" {
		fatalf("--machine is required")
	}
	client := newClient(common, ownerToken(*common.ownerTokenFile))
	machine, err := client.GetMachine(ctx, *machineID)
	fatalIf(err)
	printJSON(machine)
}

func machineRevoke(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("machine-revoke", flag.ExitOnError)
	common := addCommon(fs)
	machineID := fs.String("machine", "", "opaque machineId")
	_ = fs.Parse(args)
	if *machineID == "" {
		fatalf("--machine is required")
	}
	client := newClient(common, ownerToken(*common.ownerTokenFile))
	fatalIf(client.RevokeMachine(ctx, *machineID))
	printJSON(map[string]any{"machineId": *machineID, "status": "revoked"})
}

func backup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "Hub data directory")
	output := fs.String("out", defaultBackupName(), "backup archive path; must be outside the Hub data directory")
	_ = fs.Parse(args)
	ctx, cancel := operationContext()
	defer cancel()
	manifest, err := opsbackup.Create(ctx, *dataDir, *output, version.Version)
	fatalIf(err)
	absolute, _ := filepath.Abs(*output)
	printJSON(map[string]any{
		"backup":    absolute,
		"format":    manifest.Format,
		"createdAt": manifest.CreatedAt,
		"version":   manifest.FastSpiderVersion,
		"files":     len(manifest.Files),
	})
	fmt.Fprintln(os.Stderr, "Backup contains Hub secrets. Store the archive as sensitive data.")
}

func backupVerify(args []string) {
	fs := flag.NewFlagSet("backup-verify", flag.ExitOnError)
	file := fs.String("file", "", "backup archive path")
	_ = fs.Parse(args)
	if *file == "" {
		fatalf("--file is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	manifest, err := opsbackup.Verify(ctx, *file)
	fatalIf(err)
	printJSON(map[string]any{
		"valid":     true,
		"format":    manifest.Format,
		"createdAt": manifest.CreatedAt,
		"version":   manifest.FastSpiderVersion,
		"files":     len(manifest.Files),
	})
}

func restore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	file := fs.String("file", "", "backup archive path")
	dataDir := fs.String("data-dir", "./data", "empty Hub data directory to restore")
	_ = fs.Parse(args)
	if *file == "" {
		fatalf("--file is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	manifest, err := opsbackup.Restore(ctx, *file, *dataDir)
	fatalIf(err)
	absolute, _ := filepath.Abs(*dataDir)
	printJSON(map[string]any{
		"restored":  absolute,
		"format":    manifest.Format,
		"createdAt": manifest.CreatedAt,
		"version":   manifest.FastSpiderVersion,
		"files":     len(manifest.Files),
	})
	fmt.Fprintln(os.Stderr, "Restore completed. Start the Hub with this data directory only after confirming the previous Hub instance is stopped.")
}

func defaultBackupName() string {
	return "fast-spider-backup-" + time.Now().UTC().Format("20060102-150405") + ".zip"
}

func operationContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func addCommon(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		hub:            fs.String("hub", "https://127.0.0.1:8787", "Hub base URL"),
		allowInsecure:  fs.Bool("allow-insecure", false, "allow http only for local development"),
		ownerTokenFile: fs.String("owner-token-file", "", "file containing the owner token; otherwise FAST_SPIDER_OWNER_TOKEN is used"),
	}
}

func newClient(common commonFlags, token string) *adminclient.Client {
	client, err := adminclient.New(*common.hub, token, *common.allowInsecure)
	fatalIf(err)
	return client
}

func ownerToken(path string) string {
	if path != "" {
		return readSecret(path)
	}
	value := strings.TrimSpace(os.Getenv("FAST_SPIDER_OWNER_TOKEN"))
	if value == "" {
		fatalf("owner token is required via FAST_SPIDER_OWNER_TOKEN or --owner-token-file")
	}
	return value
}

func readSecret(path string) string {
	raw, err := os.ReadFile(path)
	fatalIf(err)
	value := strings.TrimSpace(string(raw))
	if value == "" {
		fatalf("secret file %s is empty", path)
	}
	return value
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	fatalIf(encoder.Encode(value))
}

func fatalIf(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: spiderctl <setup-url|bootstrap|enrollment-create|machine-list|machine-get|machine-revoke|backup|backup-verify|restore|version> [flags]")
}
