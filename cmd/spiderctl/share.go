package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
	"github.com/isguang2024/fast-spider/internal/version"
)

const (
	defaultShareListen     = "127.0.0.1:8787"
	shareStartupTimeout    = 30 * time.Second
	shareBootstrapTimeout  = 30 * time.Second
	shareHTTPBodyLimit     = 2 << 20
	shareTunnelAPIAddress  = "http://127.0.0.1:4040/api/tunnels"
	shareDefaultNodeBinary = "fast-spider-node"
	shareDefaultHubEnv     = "FAST_SPIDER_HUB_BIN"
	shareDefaultSourceEnv  = "FAST_SPIDER_SOURCE_ROOT"
	shareSafePrompt        = "Inspect this repository and summarize its structure. Do not make changes."
)

var (
	cloudflareURLPattern = regexp.MustCompile(`(?i)https://[a-z0-9-]+\.trycloudflare\.com(?:/[^\s]*)?`)
	csrfTokenPattern     = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)
	createdTokenPattern  = regexp.MustCompile(`(?s)<textarea id="created-token"[^>]*>(.*?)</textarea>`)
	shareSafeArgPattern  = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,\\-]+$`)
)

type shareOptions struct {
	Project    string
	Tunnel     string
	Listen     string
	DataDir    string
	HubBinary  string
	NodeBinary string
	SourceRoot string
}

type shareCommandSpec struct {
	Path string
	Args []string
	Dir  string
}

type shareProcess struct {
	cmd      *exec.Cmd
	exited   chan struct{}
	waitMu   sync.Mutex
	waitErr  error
	stopOnce sync.Once
}

type shareTunnel struct {
	process   *shareProcess
	publicURL string
}

func share(args []string) {
	opts, err := parseShareOptions(args)
	fatalIf(err)
	fatalIf(runShare(opts))
}

func parseShareOptions(args []string) (shareOptions, error) {
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts := shareOptions{}
	fs.StringVar(&opts.Project, "project", ".", "project directory to bind in Project mode")
	fs.StringVar(&opts.Tunnel, "tunnel", "none", "tunnel mode: none, cloudflare, or ngrok")
	fs.StringVar(&opts.Listen, "listen", defaultShareListen, "loopback Hub listen address")
	fs.StringVar(&opts.DataDir, "data-dir", "", "Hub data directory; omitted uses a temporary share profile")
	fs.StringVar(&opts.HubBinary, "hub-bin", "", "optional fast-spider-hub executable path")
	fs.StringVar(&opts.NodeBinary, "node-bin", "", "optional fast-spider-node executable path used in the printed command")
	fs.StringVar(&opts.SourceRoot, "source-root", "", "Fast Spider source root used when Hub/Node binaries are unavailable")
	if err := fs.Parse(args); err != nil {
		return shareOptions{}, err
	}
	if fs.NArg() != 0 {
		return shareOptions{}, fmt.Errorf("share does not accept positional arguments")
	}
	if strings.TrimSpace(opts.Project) == "" {
		return shareOptions{}, errors.New("--project must not be empty")
	}
	switch strings.ToLower(strings.TrimSpace(opts.Tunnel)) {
	case "none", "cloudflare", "ngrok":
		opts.Tunnel = strings.ToLower(strings.TrimSpace(opts.Tunnel))
	default:
		return shareOptions{}, fmt.Errorf("--tunnel must be none, cloudflare, or ngrok")
	}
	if _, _, err := shareLocalURL(opts.Listen); err != nil {
		return shareOptions{}, err
	}
	return opts, nil
}

func runShare(opts shareOptions) error {
	projectRoot, err := resolveShareProject(opts.Project)
	if err != nil {
		return err
	}
	localURL, _, err := shareLocalURL(opts.Listen)
	if err != nil {
		return err
	}

	dataDir, cleanupDataDir, err := prepareShareDataDir(opts.DataDir, projectRoot)
	if err != nil {
		return err
	}
	defer cleanupDataDir()

	hubBase, err := findShareHubCommand(opts)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tunnel, err := startShareTunnel(ctx, opts.Tunnel, opts.Listen)
	if err != nil {
		return err
	}
	if tunnel != nil {
		defer tunnel.process.Stop()
	}
	publicURL := localURL
	if tunnel != nil {
		publicURL = tunnel.publicURL
	}
	allowedHosts, err := shareAllowedHosts(publicURL)
	if err != nil {
		return err
	}

	adminPassword, err := security.RandomOpaque("share_admin_")
	if err != nil {
		return err
	}
	hubSpec := shareHubSpec(hubBase, opts.Listen, dataDir, publicURL, allowedHosts)
	hubProcess, err := startShareProcess(hubSpec, shareEnvironment(adminPassword))
	if err != nil {
		return fmt.Errorf("start Hub: %w", err)
	}
	defer hubProcess.Stop()

	if err := waitShareHubReady(ctx, hubProcess, localURL); err != nil {
		return err
	}
	connectionToken, err := initializeShareOwner(ctx, localURL, dataDir)
	if err != nil {
		return err
	}

	nodeSourceRoot := hubBase.Dir
	if nodeSourceRoot == "" {
		nodeSourceRoot = findShareSourceRoot(opts.SourceRoot)
	}
	nodeCommand, nodeRunFrom := shareNodeCommand(opts, nodeSourceRoot, publicURL, connectionToken, projectRoot)
	fmt.Println("Fast Spider ready")
	fmt.Println()
	fmt.Printf("Mode: Project (%s)\n", projectRoot)
	fmt.Printf("Tunnel: %s\n", opts.Tunnel)
	fmt.Printf("Console URL: %s\n", publicURL)
	fmt.Printf("MCP URL: %s/mcp\n", strings.TrimRight(publicURL, "/"))
	fmt.Println("Auth type: Bearer token (Node registration)")
	fmt.Println("MCP auth: OAuth (the MCP URL does not accept the Node token)")
	fmt.Printf("Credential: %s\n", connectionToken)
	fmt.Printf("Hub URL for Node: %s\n", publicURL)
	if nodeRunFrom != "" {
		fmt.Printf("Run Node command from: %s\n", nodeRunFrom)
	}
	fmt.Printf("Node command: %s\n", nodeCommand)
	fmt.Println("First safe prompt:")
	fmt.Println(shareSafePrompt)
	fmt.Println()
	fmt.Fprintln(os.Stderr, "The Node credential is shown once. Keep it private; press Ctrl-C here to stop the temporary Hub and tunnel.")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hubProcess.exited:
			if err := hubProcess.waitError(); err != nil {
				return fmt.Errorf("Hub stopped: %w", err)
			}
			return errors.New("Hub stopped unexpectedly")
		case <-shareProcessExited(tunnel):
			return errors.New("tunnel stopped unexpectedly")
		}
	}
}

func resolveShareProject(raw string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("resolve --project: %w", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve --project: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat --project: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("--project must point to a directory")
	}
	return filepath.Clean(real), nil
}

func prepareShareDataDir(raw, projectRoot string) (string, func(), error) {
	if strings.TrimSpace(raw) == "" {
		dir, err := os.MkdirTemp("", "fast-spider-share-")
		if err != nil {
			return "", func() {}, fmt.Errorf("create temporary share data directory: %w", err)
		}
		if err := validateShareDataBoundary(dir, projectRoot); err != nil {
			_ = os.RemoveAll(dir)
			return "", func() {}, err
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	dir, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", func() {}, fmt.Errorf("resolve --data-dir: %w", err)
	}
	if err := validateShareDataBoundary(dir, projectRoot); err != nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create --data-dir: %w", err)
	}
	if err := validateShareDataBoundary(dir, projectRoot); err != nil {
		return "", func() {}, err
	}
	return filepath.Clean(dir), func() {}, nil
}

func validateShareDataBoundary(dataDir, projectRoot string) error {
	if sharePathWithin(projectRoot, dataDir) {
		return errors.New("--data-dir must stay outside --project so the project policy cannot read Hub secrets")
	}
	resolved, err := resolveShareExistingAncestor(dataDir)
	if err != nil {
		return fmt.Errorf("validate --data-dir: %w", err)
	}
	if sharePathWithin(projectRoot, resolved) {
		return errors.New("--data-dir resolves inside --project")
	}
	return nil
}

func resolveShareExistingAncestor(path string) (string, error) {
	candidate := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return candidate, nil
		}
		candidate = parent
	}
}

func sharePathWithin(root, target string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func shareLocalURL(listen string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || host == "" || portText == "" {
		return "", 0, errors.New("--listen must be a loopback host and TCP port, for example 127.0.0.1:8787")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("--listen port must be between 1 and 65535")
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", 0, errors.New("share only supports loopback --listen addresses")
		}
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, portText)}).String(), port, nil
}

func shareAllowedHosts(publicURL string) ([]string, error) {
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("share public URL must contain a host")
	}
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	seen := map[string]struct{}{}
	for _, host := range hosts {
		seen[host] = struct{}{}
	}
	publicHost := strings.ToLower(parsed.Hostname())
	if publicHost != "" {
		if _, ok := seen[publicHost]; !ok {
			hosts = append(hosts, publicHost)
		}
	}
	return hosts, nil
}

func findShareHubCommand(opts shareOptions) (shareCommandSpec, error) {
	raw := strings.TrimSpace(opts.HubBinary)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(shareDefaultHubEnv))
	}
	if raw != "" {
		path, err := resolveShareExecutable(raw)
		if err != nil {
			return shareCommandSpec{}, fmt.Errorf("Hub executable: %w", err)
		}
		return shareCommandSpec{Path: path}, nil
	}
	if path, err := exec.LookPath("fast-spider-hub"); err == nil {
		return shareCommandSpec{Path: path}, nil
	}
	if root := findShareSourceRoot(opts.SourceRoot); root != "" {
		goPath, err := exec.LookPath("go")
		if err != nil {
			return shareCommandSpec{}, errors.New("share needs Go in PATH when fast-spider-hub is not installed")
		}
		return shareCommandSpec{Path: goPath, Args: []string{"run", "./cmd/hub"}, Dir: root}, nil
	}
	return shareCommandSpec{}, errors.New("cannot start Hub: install fast-spider-hub, set FAST_SPIDER_HUB_BIN, or run from the Fast Spider source tree (or pass --source-root)")
}

func findShareSourceRoot(raw string) string {
	candidates := []string{}
	if strings.TrimSpace(raw) != "" {
		candidates = append(candidates, raw)
	}
	if env := strings.TrimSpace(os.Getenv(shareDefaultSourceEnv)); env != "" {
		candidates = append(candidates, env)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if isShareSourceRoot(absolute) {
			return filepath.Clean(absolute)
		}
	}
	return ""
}

func isShareSourceRoot(root string) bool {
	for _, path := range []string{"go.mod", "cmd/hub/main.go", "cmd/node/main.go"} {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func resolveShareExecutable(raw string) (string, error) {
	if strings.ContainsAny(raw, `/\\`) || filepath.IsAbs(raw) {
		absolute, err := filepath.Abs(raw)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", errors.New("path is a directory")
		}
		return absolute, nil
	}
	path, err := exec.LookPath(raw)
	if err != nil {
		return "", err
	}
	return path, nil
}

func shareHubSpec(base shareCommandSpec, listen, dataDir, publicURL string, allowedHosts []string) shareCommandSpec {
	spec := base
	spec.Args = append([]string{}, base.Args...)
	spec.Args = append(spec.Args,
		"--listen", listen,
		"--data-dir", dataDir,
		"--allowed-hosts", strings.Join(allowedHosts, ","),
		"--public-base-url", publicURL,
		"--oauth-redirect-hosts", "chatgpt.com,localhost,127.0.0.1,::1",
	)
	return spec
}

func shareEnvironment(adminPassword string) []string {
	env := os.Environ()
	for index, item := range env {
		if strings.HasPrefix(item, "FAST_SPIDER_ADMIN_PASSWORD=") {
			env[index] = "FAST_SPIDER_ADMIN_PASSWORD=" + adminPassword
			return env
		}
	}
	return append(env, "FAST_SPIDER_ADMIN_PASSWORD="+adminPassword)
}

func startShareProcess(spec shareCommandSpec, env []string) (*shareProcess, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &shareProcess{cmd: cmd, exited: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.exited)
	}()
	return process, nil
}

func (p *shareProcess) waitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func (p *shareProcess) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
	<-p.exited
}

func shareProcessExited(tunnel *shareTunnel) <-chan struct{} {
	if tunnel == nil || tunnel.process == nil {
		return nil
	}
	return tunnel.process.exited
}

func startShareTunnel(ctx context.Context, kind, listen string) (*shareTunnel, error) {
	if kind == "none" {
		return nil, nil
	}
	binary, err := shareTunnelBinary(kind, exec.LookPath)
	if err != nil {
		return nil, err
	}
	origin := "http://" + listen
	args := shareTunnelArgs(kind, origin)
	cmd := exec.Command(binary, args...)
	if kind == "cloudflare" {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		process, err := startSharePipeProcess(cmd)
		if err != nil {
			return nil, err
		}
		lines := make(chan string, 128)
		go forwardShareLines(stdout, lines)
		go forwardShareLines(stderr, lines)
		publicURL, err := waitCloudflareURL(ctx, process, lines)
		if err != nil {
			process.Stop()
			return nil, err
		}
		return &shareTunnel{process: process, publicURL: publicURL}, nil
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	process, err := startSharePipeProcess(cmd)
	if err != nil {
		return nil, err
	}
	publicURL, err := waitNgrokURL(ctx, process, origin)
	if err != nil {
		process.Stop()
		return nil, err
	}
	return &shareTunnel{process: process, publicURL: publicURL}, nil
}

func shareTunnelBinary(kind string, lookup func(string) (string, error)) (string, error) {
	name := kind
	hint := ""
	switch kind {
	case "cloudflare":
		name = "cloudflared"
		hint = "Install cloudflared and make sure it is in PATH (see docs/free-local-deployment.md)."
	case "ngrok":
		name = "ngrok"
		hint = "Install ngrok and make sure it is in PATH (see docs/free-local-deployment.md)."
	default:
		return "", fmt.Errorf("unsupported tunnel %q", kind)
	}
	path, err := lookup(name)
	if err != nil {
		return "", fmt.Errorf("%s tunnel requires %s in PATH. %s", kind, name, hint)
	}
	return path, nil
}

func shareTunnelArgs(kind, origin string) []string {
	switch kind {
	case "cloudflare":
		return []string{"tunnel", "--no-autoupdate", "--url", origin}
	case "ngrok":
		return []string{"http", "--log=stdout", "--log-format=json", "--host-header=rewrite", origin}
	default:
		return nil
	}
}

func startSharePipeProcess(cmd *exec.Cmd) (*shareProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &shareProcess{cmd: cmd, exited: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.exited)
	}()
	return process, nil
}

func forwardShareLines(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		log.Printf("tunnel: %s", line)
		select {
		case lines <- line:
		default:
		}
	}
}

func waitCloudflareURL(ctx context.Context, process *shareProcess, lines <-chan string) (string, error) {
	startupCtx, cancel := context.WithTimeout(ctx, shareStartupTimeout)
	defer cancel()
	for {
		select {
		case line := <-lines:
			match := cloudflareURLPattern.FindString(line)
			if match != "" {
				return normalizeSharePublicURL(strings.TrimRight(match, ".,);"))
			}
		case <-process.exited:
			if err := process.waitError(); err != nil {
				return "", fmt.Errorf("cloudflared stopped before publishing a URL: %w", err)
			}
			return "", errors.New("cloudflared stopped before publishing a URL")
		case <-startupCtx.Done():
			return "", errors.New("timed out waiting for the Cloudflare Quick Tunnel URL")
		}
	}
}

func waitNgrokURL(ctx context.Context, process *shareProcess, origin string) (string, error) {
	startupCtx, cancel := context.WithTimeout(ctx, shareStartupTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		request, err := http.NewRequestWithContext(startupCtx, http.MethodGet, shareTunnelAPIAddress, nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var payload struct {
					Tunnels []struct {
						PublicURL string `json:"public_url"`
						Config    struct {
							Addr string `json:"addr"`
						} `json:"config"`
					} `json:"tunnels"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
				_ = response.Body.Close()
				if decodeErr == nil {
					for _, tunnel := range payload.Tunnels {
						if shareTunnelOriginMatches(tunnel.Config.Addr, origin) && strings.HasPrefix(strings.ToLower(strings.TrimSpace(tunnel.PublicURL)), "https://") {
							return normalizeSharePublicURL(tunnel.PublicURL)
						}
					}
				}
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-process.exited:
			timer.Stop()
			if err := process.waitError(); err != nil {
				return "", fmt.Errorf("ngrok stopped before publishing a URL: %w", err)
			}
			return "", errors.New("ngrok stopped before publishing a URL")
		case <-startupCtx.Done():
			timer.Stop()
			return "", errors.New("timed out waiting for the ngrok local API at 127.0.0.1:4040")
		case <-timer.C:
		}
	}
}

func shareTunnelOriginMatches(configAddr, origin string) bool {
	configAddr = strings.TrimRight(strings.TrimSpace(configAddr), "/")
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if configAddr == "" || origin == "" {
		return false
	}
	if strings.EqualFold(configAddr, origin) {
		return true
	}
	return strings.EqualFold("http://"+configAddr, origin) || strings.EqualFold("https://"+configAddr, origin)
}

func normalizeSharePublicURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("tunnel URL must be an absolute http(s) URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return "", errors.New("public non-loopback tunnel URLs must use https")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func waitShareHubReady(ctx context.Context, process *shareProcess, localURL string) error {
	startupCtx, cancel := context.WithTimeout(ctx, shareStartupTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		request, err := http.NewRequestWithContext(startupCtx, http.MethodGet, strings.TrimRight(localURL, "/")+"/readyz", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-process.exited:
			if err := process.waitError(); err != nil {
				return fmt.Errorf("Hub stopped before readiness: %w", err)
			}
			return errors.New("Hub stopped before readiness")
		case <-startupCtx.Done():
			return errors.New("timed out waiting for Hub readiness")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func initializeShareOwner(ctx context.Context, localURL, dataDir string) (string, error) {
	bootstrapPath := filepath.Join(dataDir, "bootstrap-token")
	bootstrapToken, err := waitShareSecret(ctx, bootstrapPath)
	if err != nil {
		return "", err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	base := strings.TrimRight(localURL, "/")
	ownerPassword, err := security.RandomOpaque("share_owner_")
	if err != nil {
		return "", err
	}
	setupValues := url.Values{
		"username":         {"shareowner"},
		"display_name":     {"Fast Spider Share Owner"},
		"password":         {ownerPassword},
		"password_confirm": {ownerPassword},
		"bootstrap_token":  {bootstrapToken},
	}
	response, _, err := shareFormRequest(ctx, client, http.MethodPost, base+"/setup", setupValues)
	if err != nil {
		return "", fmt.Errorf("initialize owner: %w", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		return "", fmt.Errorf("initialize owner failed with HTTP %d", response.StatusCode)
	}
	if len(jar.Cookies(mustParseURL(base))) == 0 {
		return "", errors.New("initialize owner did not return a web session cookie")
	}

	response, tokenPage, err := shareFormRequest(ctx, client, http.MethodGet, base+"/app/access/tokens", nil)
	if err != nil {
		return "", fmt.Errorf("open connection token page: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("open connection token page failed with HTTP %d", response.StatusCode)
	}
	csrfMatches := csrfTokenPattern.FindSubmatch(tokenPage)
	if len(csrfMatches) != 2 {
		return "", errors.New("connection token page did not contain a CSRF token")
	}
	response, tokenBody, err := shareFormRequest(ctx, client, http.MethodPost, base+"/app/tokens", url.Values{
		"csrf_token":   {string(csrfMatches[1])},
		"label":        {"share-node"},
		"expires_days": {"30"},
	})
	if err != nil {
		return "", fmt.Errorf("create connection token: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create connection token failed with HTTP %d", response.StatusCode)
	}
	matches := createdTokenPattern.FindSubmatch(tokenBody)
	if len(matches) != 2 {
		return "", errors.New("connection token was not returned by the Hub")
	}
	token := strings.TrimSpace(html.UnescapeString(string(matches[1])))
	if !strings.HasPrefix(token, "ctk_") {
		return "", errors.New("Hub returned an invalid connection token")
	}
	return token, nil
}

func waitShareSecret(ctx context.Context, path string) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, shareBootstrapTimeout)
	defer cancel()
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(raw))
			if value != "" {
				return value, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read Hub bootstrap token: %w", err)
		}
		select {
		case <-deadline.Done():
			return "", errors.New("timed out waiting for the Hub bootstrap token")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func shareFormRequest(ctx context.Context, client *http.Client, method, endpoint string, values url.Values) (*http.Response, []byte, error) {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, nil, err
	}
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", "fast-spider-share/"+version.Version)
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, shareHTTPBodyLimit+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > shareHTTPBodyLimit {
		return nil, nil, errors.New("Hub response exceeds share limit")
	}
	return response, data, nil
}

func mustParseURL(raw string) *url.URL {
	parsed, _ := url.Parse(raw)
	return parsed
}

func shareNodeCommand(opts shareOptions, sourceRoot, hubURL, token, projectRoot string) (string, string) {
	command := shareDefaultNodeBinary
	runFrom := ""
	if strings.TrimSpace(opts.NodeBinary) != "" {
		command = opts.NodeBinary
	} else if path, err := exec.LookPath(shareDefaultNodeBinary); err == nil {
		command = path
	} else if sourceRoot != "" && isShareSourceRoot(sourceRoot) {
		command = "go run ./cmd/node"
		runFrom = sourceRoot
	}
	commandText := command
	if command != "go run ./cmd/node" {
		commandText = shareQuote(command)
	}
	parts := []string{commandText, "connect", "--hub", shareQuote(hubURL), "--token", shareQuote(token), "--project-root", shareQuote(projectRoot)}
	if strings.HasPrefix(strings.ToLower(hubURL), "http://") {
		parts = append(parts, "--allow-insecure")
	}
	return strings.Join(parts, " "), runFrom
}

func shareQuote(value string) string {
	if value != "" && shareSafeArgPattern.MatchString(value) {
		return value
	}
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
