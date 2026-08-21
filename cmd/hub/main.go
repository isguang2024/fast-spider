package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/version"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8787", "Hub HTTP listen address; production should stay on loopback behind TLS reverse proxy")
	dataDir := flag.String("data-dir", "./data", "Hub data directory")
	releaseDir := flag.String("release-dir", "", "signed Node/component release directory; defaults to <data-dir>-releases")
	allowedHosts := flag.String("allowed-hosts", "localhost,127.0.0.1", "comma-separated Host allowlist; use the public Hub hostname in production")
	publicBaseURL := flag.String("public-base-url", "", "public Hub base URL used for MCP OAuth discovery, for example https://sharedservices.example/fast-spider")
	oauthRedirectHosts := flag.String("oauth-redirect-hosts", "chatgpt.com,localhost,127.0.0.1,::1", "comma-separated OAuth redirect host allowlist")
	adminPassword := flag.String("admin-password", "", "one-time administrator password; prefer FAST_SPIDER_ADMIN_PASSWORD to avoid process listings")
	showVersion := flag.Bool("version", false, "print Fast Spider version and exit")
	flag.Parse()
	if strings.TrimSpace(*adminPassword) == "" {
		*adminPassword = os.Getenv("FAST_SPIDER_ADMIN_PASSWORD")
	}
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	publicBase, err := normalizePublicBaseURL(*publicBaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid public base URL:", err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	absoluteDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		fatal(logger, "resolve data directory", err)
	}
	absoluteReleaseDir := strings.TrimSpace(*releaseDir)
	if absoluteReleaseDir != "" {
		absoluteReleaseDir, err = filepath.Abs(absoluteReleaseDir)
		if err != nil {
			fatal(logger, "resolve release directory", err)
		}
	}
	databasePath := filepath.Join(absoluteDataDir, "hub.db")
	st, err := store.Open(ctx, databasePath)
	if err != nil {
		fatal(logger, "open hub store", err)
	}
	defer st.Close()

	service, err := core.New(st, registry.New(), core.Config{DataDir: absoluteDataDir, ReleaseDir: absoluteReleaseDir, Version: version.Version})
	if err != nil {
		fatal(logger, "initialize hub core", err)
	}
	if err := service.EnsureAdminAccount(ctx, *adminPassword); err != nil {
		fatal(logger, "initialize admin account", err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		fatal(logger, "initialize owner bootstrap", err)
	}
	if bootstrapToken != "" {
		logger.Warn("owner account setup required", "tokenFile", filepath.Join(absoluteDataDir, "bootstrap-token"), "expiresIn", "30m")
	}

	go service.StartMaintenance(ctx)
	hub := server.New(service, server.Config{
		ListenAddr:         *listen,
		AllowedHosts:       splitCSV(*allowedHosts),
		PublicBaseURL:      publicBase,
		OAuthRedirectHosts: splitCSV(*oauthRedirectHosts),
		Logger:             logger,
	})
	go hub.StartMaintenance(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hub.Shutdown(shutdownCtx); err != nil {
			logger.Error("hub shutdown failed", "error", err)
		}
	}()

	logger.Info("fast spider hub starting", "version", version.Version, "listen", *listen, "dataDir", absoluteDataDir, "releaseDir", service.ReleaseDir(), "hubFingerprint", service.HubFingerprint())
	if err := hub.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, "hub server failed", err)
	}
}

func normalizePublicBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must be an absolute http(s) URL without credentials, query, or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("public non-loopback URLs must use https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func splitCSV(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func fatal(logger *slog.Logger, message string, err error) {
	logger.Error(message, "error", err)
	fmt.Fprintln(os.Stderr, message+":", err)
	os.Exit(1)
}
