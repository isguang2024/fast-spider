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

	"github.com/isguang2024/fast-spider/internal/opsbackup"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/version"
)

const defaultReleaseBackupKeep = 3

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "setup-url":
		setupURL(os.Args[2:])
	case "backup":
		backup(os.Args[2:])
	case "backup-verify":
		backupVerify(os.Args[2:])
	case "backup-prune":
		backupPrune(os.Args[2:])
	case "staging-prune":
		stagingPrune(os.Args[2:])
	case "node-update-push":
		nodeUpdatePush(os.Args[2:])
	case "restore":
		restore(os.Args[2:])
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
		"backup": absolute, "format": manifest.Format, "createdAt": manifest.CreatedAt,
		"version": manifest.FastSpiderVersion, "files": len(manifest.Files),
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
		"valid": true, "format": manifest.Format, "createdAt": manifest.CreatedAt,
		"version": manifest.FastSpiderVersion, "files": len(manifest.Files),
	})
}

func backupPrune(args []string) {
	fs := flag.NewFlagSet("backup-prune", flag.ExitOnError)
	directory := fs.String("dir", "", "absolute directory containing standard release backups")
	keep := fs.Int("keep", defaultReleaseBackupKeep, "number of newest verified release backups to keep")
	_ = fs.Parse(args)
	if strings.TrimSpace(*directory) == "" {
		fatalf("--dir is required")
	}
	if !filepath.IsAbs(*directory) {
		fatalf("--dir must be an absolute path")
	}
	ctx, cancel := operationContext()
	defer cancel()
	result, err := runBackupPrune(ctx, *directory, *keep)
	if err == nil || result.CandidateCount > 0 {
		printJSON(result)
	}
	fatalIf(err)
}

func runBackupPrune(ctx context.Context, directory string, keep int) (opsbackup.PruneResult, error) {
	if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
		return opsbackup.PruneResult{}, fmt.Errorf("backup prune directory must be an absolute path")
	}
	return opsbackup.PruneReleaseBackups(ctx, directory, keep)
}

func stagingPrune(args []string) {
	fs := flag.NewFlagSet("staging-prune", flag.ExitOnError)
	directory := fs.String("dir", "", "absolute release staging root")
	layout := fs.String("layout", "", "staging layout: local or server")
	through := fs.String("through", "", "highest completed three-part semantic version to prune")
	apply := fs.Bool("apply", false, "apply the planned deletion; default is plan-only")
	_ = fs.Parse(args)
	ctx, cancel := operationContext()
	defer cancel()
	result, err := runStagingPrune(ctx, *directory, *layout, *through, *apply)
	if err == nil || result.CandidateCount > 0 {
		printJSON(result)
	}
	fatalIf(err)
}

func runStagingPrune(ctx context.Context, directory, layout, through string, apply bool) (opsbackup.StagingPruneResult, error) {
	if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
		return opsbackup.StagingPruneResult{}, fmt.Errorf("staging prune directory must be an absolute path")
	}
	return opsbackup.PruneReleaseStaging(ctx, opsbackup.StagingPruneOptions{
		Directory:      directory,
		Layout:         opsbackup.StagingLayout(strings.TrimSpace(layout)),
		ThroughVersion: strings.TrimSpace(through),
		Apply:          apply,
	})
}

func nodeUpdatePush(args []string) {
	fs := flag.NewFlagSet("node-update-push", flag.ExitOnError)
	releaseDir := fs.String("release-dir", "", "absolute Hub release directory")
	platform := fs.String("platform", "windows-amd64", "Node release platform")
	_ = fs.Parse(args)
	marker, err := runNodeUpdatePush(*releaseDir, *platform, time.Now().UTC())
	fatalIf(err)
	printJSON(marker)
}

func runNodeUpdatePush(releaseDir, platform string, now time.Time) (releaseinfo.NodeUpdatePush, error) {
	if strings.TrimSpace(releaseDir) == "" || !filepath.IsAbs(releaseDir) {
		return releaseinfo.NodeUpdatePush{}, fmt.Errorf("release directory must be an absolute path")
	}
	return releaseinfo.CreateNodeUpdatePush(releaseDir, platform, now)
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
		"restored": absolute, "format": manifest.Format, "createdAt": manifest.CreatedAt,
		"version": manifest.FastSpiderVersion, "files": len(manifest.Files),
	})
	fmt.Fprintln(os.Stderr, "Restore completed. Start the Hub with this data directory only after confirming the previous Hub instance is stopped.")
}

func defaultBackupName() string {
	return "fast-spider-backup-" + time.Now().UTC().Format("20060102-150405") + ".zip"
}

func operationContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	fmt.Fprintln(os.Stderr, "usage: spiderctl <setup-url|backup|backup-verify|backup-prune|staging-prune|node-update-push|restore|version> [flags]")
}
