package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"amdl/internal/api"
	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/events"
	"amdl/internal/hooks"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/mocktool"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfgPath := os.Getenv("AMDL_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if v := os.Getenv("AMDL_MOCK_LISTEN"); v != "" {
		cfg.Server.Listen = v
	}
	if v := os.Getenv("AMDL_MOCK_DB"); v != "" {
		cfg.Database.Path = v
	}
	// Enable the developer-token endpoint in the mock server without requiring
	// real Apple Music signing credentials.
	cfg.Catalog.AppleMusicPrivateKeyPath = "mock-private-key.p8"
	cfg.Catalog.AppleMusicKeyID = "MOCKKEYID"
	cfg.Catalog.AppleMusicTeamID = "MOCKTEAMID"
	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o755); err != nil {
		logger.Error("create database dir", "error", err)
		os.Exit(1)
	}

	store, err := db.Open(cfg.Database.Path)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	hub := events.NewHub()
	services := mocktool.NewServices(cfg)
	manager := jobs.NewManager(store, hub, services.Processor, cfg.Download.MaxRunningJobs, logger)
	hooksCfgPath := os.Getenv("AMDL_HOOKS_CONFIG")
	if hooksCfgPath == "" {
		hooksCfgPath = "configs/hooks.yaml"
	}
	hooksCfg, err := hooks.LoadConfig(hooksCfgPath)
	if err != nil {
		logger.Error("load hooks config", "error", err)
		os.Exit(1)
	}
	hookDispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, logger)
	manager.SetHooks(hookDispatcher)
	toolChecker := media.NewToolChecker(cfg.Tools)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if recovered, err := manager.RecoverUnfinished(ctx); err != nil {
		logger.Error("recover unfinished jobs", "error", err)
		os.Exit(1)
	} else if recovered > 0 {
		logger.Info("recovered unfinished mock jobs", "count", recovered)
	}
	manager.Start(ctx)

	httpServer := &http.Server{Addr: cfg.Server.Listen, Handler: api.NewServer(cfg, store, hub, manager, services.Wrapper, services.Quality, services.Token, toolChecker, logger).Routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("amdl mock backend listening", "addr", cfg.Server.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	hookDispatcher.Shutdown(shutdownCtx)
}
