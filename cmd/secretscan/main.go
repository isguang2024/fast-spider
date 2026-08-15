package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/isguang2024/fast-spider/internal/secretscan"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secretscan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	history := flags.Bool("history", false, "also scan every blob in the Git object database")
	tree := flags.String("tree", "", "scan an exact filesystem tree instead of a Git repository")
	repository := flags.String("repo", ".", "Git repository to scan")
	markerFile := flags.String("markers", "", "optional local private-marker file")
	selfTest := flags.Bool("self-test", false, "run the synthetic detection and redaction self-test")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: secretscan [--history] [--repo <dir>] [--markers <file>]")
		fmt.Fprintln(stderr, "       secretscan --tree <dir> [--markers <file>]")
		fmt.Fprintln(stderr, "       secretscan --self-test")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "secretscan: positional arguments are not supported")
		return 2
	}
	selected := 0
	if strings.TrimSpace(*tree) != "" {
		selected++
	}
	if *selfTest {
		selected++
	}
	if selected > 1 || (*selfTest && (*history || *repository != "." || *markerFile != "")) || (*tree != "" && (*history || *repository != ".")) {
		fmt.Fprintln(stderr, "secretscan: --tree and --self-test are mutually exclusive with repository options")
		return 2
	}
	if *selfTest {
		if err := secretscan.SelfTest(ctx); err != nil {
			fmt.Fprintln(stderr, "secretscan: self-test failed")
			return 2
		}
		fmt.Fprintln(stdout, "PASS: secretscan synthetic self-test")
		return 0
	}

	options := secretscan.Options{MarkerFile: *markerFile}
	var (
		findings []secretscan.Finding
		err      error
	)
	if *tree != "" {
		if options.MarkerFile == "" {
			options.MarkerFile = defaultMarkerFile(".")
		}
		findings, err = secretscan.ScanTree(ctx, *tree, options)
	} else {
		findings, err = secretscan.ScanRepository(ctx, *repository, *history, options)
	}
	if err != nil {
		fmt.Fprintln(stderr, "secretscan: scan failed:", safeError(err))
		return 2
	}
	if len(findings) != 0 {
		if err := secretscan.WriteFindings(stderr, findings); err != nil {
			fmt.Fprintln(stderr, "secretscan: write findings failed")
			return 2
		}
		fmt.Fprintf(stderr, "FAIL: secretscan found %d potential secret(s)\n", len(findings))
		return 1
	}
	if *tree != "" {
		fmt.Fprintln(stdout, "PASS: secretscan tree")
	} else if *history {
		fmt.Fprintln(stdout, "PASS: secretscan worktree, index, and history")
	} else {
		fmt.Fprintln(stdout, "PASS: secretscan worktree and index")
	}
	return 0
}

func defaultMarkerFile(directory string) string {
	root, err := filepath.Abs(directory)
	if err != nil {
		return ""
	}
	candidate := filepath.Join(root, ".local", "public-private-markers.txt")
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return ""
	}
	return candidate
}

func safeError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := err.Error()
	if strings.ContainsAny(message, "\r\n\x00") {
		return "scanner operation failed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "scan canceled or timed out"
	}
	return message
}
