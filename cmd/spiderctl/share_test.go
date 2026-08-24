package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharePublicURLAndAllowedHost(t *testing.T) {
	publicURL, err := normalizeSharePublicURL("https://demo.trycloudflare.com/")
	if err != nil {
		t.Fatal(err)
	}
	if publicURL != "https://demo.trycloudflare.com" {
		t.Fatalf("publicURL=%q", publicURL)
	}
	hosts, err := shareAllowedHosts(publicURL)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(hosts, ",")
	for _, expected := range []string{"localhost", "127.0.0.1", "::1", "demo.trycloudflare.com"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("allowed hosts %q does not contain %q", joined, expected)
		}
	}
	if _, err := normalizeSharePublicURL("http://public.example"); err == nil {
		t.Fatal("non-loopback http URL unexpectedly accepted")
	}
}

func TestShareAllowedHostLocalURL(t *testing.T) {
	localURL, _, err := shareLocalURL("127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if localURL != "http://127.0.0.1:8787" {
		t.Fatalf("localURL=%q", localURL)
	}
	if _, _, err := shareLocalURL("0.0.0.0:8787"); err == nil {
		t.Fatal("wildcard listen address unexpectedly accepted")
	}
}

func TestShareTunnelDependencyDetection(t *testing.T) {
	lookup := func(name string) (string, error) {
		return "", errors.New("not found: " + name)
	}
	for _, kind := range []string{"cloudflare", "ngrok"} {
		_, err := shareTunnelBinary(kind, lookup)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), kind) {
			t.Fatalf("kind=%s error=%v", kind, err)
		}
	}
}

func TestShareTunnelCommandGeneration(t *testing.T) {
	cloudflare := shareTunnelArgs("cloudflare", "http://127.0.0.1:8787")
	if strings.Join(cloudflare, " ") != "tunnel --no-autoupdate --url http://127.0.0.1:8787" {
		t.Fatalf("cloudflare args=%v", cloudflare)
	}
	ngrok := shareTunnelArgs("ngrok", "http://127.0.0.1:8787")
	joined := strings.Join(ngrok, " ")
	for _, expected := range []string{"http", "--log-format=json", "--host-header=rewrite", "http://127.0.0.1:8787"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("ngrok args=%q missing %q", joined, expected)
		}
	}
}

func TestShareProjectModeDataBoundary(t *testing.T) {
	projectRoot := t.TempDir()
	inside := filepath.Join(projectRoot, ".fast-spider", "share")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateShareDataBoundary(inside, projectRoot); err == nil {
		t.Fatal("data directory inside project unexpectedly accepted")
	}
	outside := filepath.Join(t.TempDir(), "share")
	if err := validateShareDataBoundary(outside, projectRoot); err != nil {
		t.Fatalf("outside data directory rejected: %v", err)
	}
}

func TestShareProjectModeDataBoundaryRejectsSymlinkAlias(t *testing.T) {
	projectRoot := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "project-link")
	if err := os.Symlink(projectRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateShareDataBoundary(filepath.Join(link, "share"), projectRoot); err == nil {
		t.Fatal("data directory through a symlink unexpectedly accepted")
	}
}

func TestShareTunnelOriginMatching(t *testing.T) {
	if !shareTunnelOriginMatches("http://127.0.0.1:8787", "http://127.0.0.1:8787") {
		t.Fatal("matching ngrok origin rejected")
	}
	if !shareTunnelOriginMatches("127.0.0.1:8787", "http://127.0.0.1:8787") {
		t.Fatal("scheme-less ngrok origin rejected")
	}
	if shareTunnelOriginMatches("http://127.0.0.1:9999", "http://127.0.0.1:8787") {
		t.Fatal("different ngrok origin accepted")
	}
}

func TestShareNodeCommandUsesProjectMode(t *testing.T) {
	sourceRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	command, runFrom := shareNodeCommand(shareOptions{}, sourceRoot, "http://127.0.0.1:8787", "ctk_test", `C:\work\repo with spaces`)
	if !strings.Contains(command, "connect") || !strings.Contains(command, "--project-root") || !strings.Contains(command, "repo with spaces") {
		t.Fatalf("node command=%q", command)
	}
	if !strings.Contains(command, "--allow-insecure") {
		t.Fatalf("local node command missing --allow-insecure: %q", command)
	}
	if runFrom != "" && runFrom != sourceRoot {
		t.Fatalf("runFrom=%q", runFrom)
	}
}

func TestShareLocalOnlyOptions(t *testing.T) {
	opts, err := parseShareOptions([]string{"--project", ".", "--tunnel", "none"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Tunnel != "none" || opts.Project != "." {
		t.Fatalf("options=%+v", opts)
	}
}
