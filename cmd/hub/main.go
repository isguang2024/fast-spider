package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	allowedHosts := flag.String("allowed-hosts", "localhost,127.0.0.1", "comma-separated Host allowlist; use the public Hub hostname in production")
	showVersion := flag.Bool("version", false, "print Fast Spider version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	absoluteDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		fatal(logger, "resolve data directory", err)
	}
	databasePath := filepath.Join(absoluteDataDir, "hub.db")
	st, err := store.Open(ctx, databasePath)
	if err != nil {
		fatal(logger, "open hub store", err)
	}
	defer st.Close()

	service, err := core.New(st, registry.New(), core.Config{DataDir: absoluteDataDir, Version: version.Version})
	if err != nil {
		fatal(logger, "initialize hub core", err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		fatal(logger, "initialize owner bootstrap", err)
	}
	if bootstrapToken != "" {
		logger.Warn("owner bootstrap required", "tokenFile", filepath.Join(absoluteDataDir, "bootstrap-token"), "expiresIn", "30m")
	}

	go service.StartMaintenance(ctx)
	hub := server.New(service, server.Config{
		ListenAddr:   *listen,
		AllowedHosts: splitCSV(*allowedHosts),
		Logger:       logger,
	})

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hub.Shutdown(shutdownCtx); err != nil {
			logger.Error("hub shutdown failed", "error", err)
		}
	}()

	logger.Info("fast spider hub starting", "version", version.Version, "listen", *listen, "dataDir", absoluteDataDir, "hubFingerprint", service.HubFingerprint())
	if err := hub.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, "hub server failed", err)
	}
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
