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
	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/wrapper"
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
	if err := os.MkdirAll(cfg.Download.DownloadsDir, 0o755); err != nil {
		logger.Error("create downloads dir", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.Download.TempDir, 0o755); err != nil {
		logger.Error("create temp dir", "error", err)
		os.Exit(1)
	}
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
	wrapperClient, err := wrapper.NewClient(cfg.Wrapper)
	if err != nil {
		logger.Error("connect wrapper-manager", "error", err)
		os.Exit(1)
	}
	defer wrapperClient.Close()

	catalog := applemusic.NewCatalogClient(cfg.Catalog, logger)
	toolChecker := media.NewToolChecker(cfg.Tools)
	downloader := media.NewDownloader(cfg, catalog, wrapperClient, toolChecker, logger)
	qualityService := media.NewQualityService(cfg, catalog)
	manager := jobs.NewManager(store, hub, downloader, cfg.Download.MaxRunningJobs, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if recovered, err := manager.RecoverUnfinished(ctx); err != nil {
		logger.Error("recover unfinished jobs", "error", err)
		os.Exit(1)
	} else if recovered > 0 {
		logger.Info("recovered unfinished jobs", "count", recovered)
	}
	manager.Start(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           api.NewServer(cfg, store, hub, manager, wrapperClient, qualityService, toolChecker, logger).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("amdl backend listening", "addr", cfg.Server.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
