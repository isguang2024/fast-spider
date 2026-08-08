package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/adminclient"
	"github.com/isguang2024/fast-spider/internal/version"
)

type commonFlags struct {
	hub           *string
	allowInsecure *bool
	ownerTokenFile *string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch os.Args[1] {
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

func addCommon(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		hub: fs.String("hub", "https://127.0.0.1:8787", "Hub base URL"),
		allowInsecure: fs.Bool("allow-insecure", false, "allow http only for local development"),
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
	fmt.Fprintln(os.Stderr, "usage: spiderctl <bootstrap|enrollment-create|machine-list|machine-get|machine-revoke|version> [flags]")
}
