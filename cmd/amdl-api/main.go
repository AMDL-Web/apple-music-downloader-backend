package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"amdl/internal/api"
	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/hooks"
	"amdl/internal/jobs"
	"amdl/internal/logging"
	"amdl/internal/media"
	"amdl/internal/wrapper"
)

func main() {
	bootstrapLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfgPath := os.Getenv("AMDL_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/config.yaml"
	}
	// The runtime config file defaults to a sibling of the startup config, so
	// a custom AMDL_CONFIG location carries both files along.
	runtimeCfgPath := os.Getenv("AMDL_RUNTIME_CONFIG")
	if runtimeCfgPath == "" {
		runtimeCfgPath = filepath.Join(filepath.Dir(cfgPath), "runtime.yaml")
	}
	// First start: create the startup config from the committed, commented
	// config.example.yaml next to it (the startup file is owner-edited from
	// then on) and the machine-managed runtime file from runtime.example.yaml.
	// A pre-split config.yaml still carrying runtime keys is migrated once:
	// split in place with a backup of the combined file left behind.
	bootstrap, err := config.EnsureFiles(cfgPath, runtimeCfgPath)
	if err != nil {
		bootstrapLogger.Error("bootstrap config", "error", err)
		os.Exit(1)
	}
	if bootstrap.CreatedStartup {
		bootstrapLogger.Info("created startup config from example", "path", cfgPath)
	}
	if bootstrap.CreatedRuntime {
		bootstrapLogger.Info("created runtime config", "path", runtimeCfgPath)
	}
	if bootstrap.MigratedLegacy {
		bootstrapLogger.Info("split legacy config into startup and runtime files",
			"startup", cfgPath, "runtime", runtimeCfgPath, "backup", bootstrap.LegacyBackupPath)
	}
	cfg, err := config.LoadPair(cfgPath, runtimeCfgPath)
	if err != nil {
		bootstrapLogger.Error("load config", "error", err)
		os.Exit(1)
	}
	logSystem, err := logging.New(cfg.Logging)
	if err != nil {
		bootstrapLogger.Error("initialize logging", "error", err)
		os.Exit(1)
	}
	defer logSystem.Close()
	slog.SetDefault(logSystem.Logger)
	logger := logSystem.Logger.With("component", "main")
	logger.Info("logging initialized", "level", cfg.Logging.Level, "format", cfg.Logging.Format, "file_enabled", cfg.Logging.FileEnabled)
	hooksCfgPath := os.Getenv("AMDL_HOOKS_CONFIG")
	if hooksCfgPath == "" {
		hooksCfgPath = "configs/hooks.yaml"
	}
	hooksCfg, err := hooks.LoadConfig(hooksCfgPath)
	if err != nil {
		logger.Error("load hooks config", "error", err)
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
	// Remove non-resumable scratch files a previous run left behind. Encrypted
	// resume-* checkpoints deliberately survive this sweep so recovered jobs can
	// continue their HLS transfer. Safe before any job has started because the
	// temp dir is single-writer.
	media.CleanupStaleTemp(cfg.Download.TempDir, logSystem.Logger.With("component", "media"))
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
	// cfgStore is the live runtime config shared by the API layer and download
	// pipeline. Process-wide concurrency pools are sized once from this startup
	// snapshot; runtime-mutable fields are read when each job starts.
	cfgStore := config.NewFileStore(cfg, runtimeCfgPath)
	wrapperClient, err := wrapper.NewClient(cfg.Wrapper, wrapper.WithDataConcurrencyLimit(cfg.Download.MaxParallelWrapperRequests))
	if err != nil {
		logger.Error("connect wrapper-manager", "error", err)
		os.Exit(1)
	}
	defer wrapperClient.Close()

	catalog := applemusic.NewCatalogClient(cfg.Catalog, logSystem.Logger.With("component", "catalog"))
	if err := catalog.InitDeveloperToken(); err != nil {
		logger.Error("sign apple music developer token", "error", err)
		os.Exit(1)
	}
	toolChecker := media.NewToolChecker(cfg.Tools)
	downloader := media.NewDownloader(cfgStore, catalog, wrapperClient, toolChecker, logSystem.Logger.With("component", "media"))
	qualityService := media.NewQualityService(cfgStore, catalog, wrapperClient)
	manager := jobs.NewManager(store, hub, downloader, cfg.Download.MaxRunningJobs, logSystem.Logger.With("component", "jobs"))
	hookDispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, logSystem.Logger.With("component", "hooks"))
	manager.SetHooks(hookDispatcher)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if recovered, err := manager.RecoverUnfinished(ctx); err != nil {
		logger.Error("recover unfinished jobs", "error", err)
		os.Exit(1)
	} else if recovered > 0 {
		logger.Info("recovered unfinished jobs", "count", recovered)
	}
	// A job may stage its scratch under a per-request temp_dir override, which
	// the configured-dir sweep above doesn't cover. Jobs that were queued or
	// running at the last shutdown are exactly the ones whose in-flight scratch
	// could have leaked on a crash; sweep their override dirs too. Still before
	// the worker pool starts, so nothing is writing to them.
	if recoverable, err := store.ListRecoverableJobs(ctx); err == nil {
		swept := map[string]bool{cfg.Download.TempDir: true}
		for _, job := range recoverable {
			dir, ok, err := recoveryTempOverride(cfg, job)
			if err != nil {
				logger.Warn("skip unsafe recovered temp override", "job_id", job.ID, "error", err)
				continue
			}
			if ok && !swept[dir] {
				swept[dir] = true
				media.CleanupStaleTemp(dir, logSystem.Logger.With("component", "media"))
			}
		}
	}
	// Worker lifetime is deliberately independent of the signal context. On
	// shutdown main first closes the HTTP listener (so no new jobs enter), then
	// explicitly cancels and joins workers before draining hooks and allowing
	// the deferred database/wrapper closes to run.
	manager.Start(context.Background())

	httpHandlerCtx, cancelHTTPHandlers := context.WithCancel(context.Background())
	defer cancelHTTPHandlers()
	var activeHTTPHandlers sync.WaitGroup
	routes := api.NewServer(cfgStore, store, hub, manager, wrapperClient, qualityService, catalog, logSystem.Logger, logSystem).Routes()
	httpServer := &http.Server{
		Addr: cfg.Server.Listen,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			activeHTTPHandlers.Add(1)
			defer activeHTTPHandlers.Done()
			routes.ServeHTTP(w, r)
		}),
		ErrorLog:          slog.NewLogLogger(logSystem.Logger.With("component", "http").Handler(), slog.LevelError),
		BaseContext:       func(net.Listener) context.Context { return httpHandlerCtx },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		// Do not set WriteTimeout: SSE and WebSocket responses are intentionally
		// long-lived and must not be cut off by a whole-response deadline.
	}
	go func() {
		logger.Info("amdl backend listening", "addr", cfg.Server.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()

	httpShutdownCtx, cancelHTTPShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := httpServer.Shutdown(httpShutdownCtx); err != nil {
		logger.Warn("http shutdown timed out", "error", err)
	}
	cancelHTTPShutdown()
	// The base context must stay live during Shutdown so in-flight ordinary
	// requests can finish inside the drain window. Shutdown does not wait for
	// hijacked WebSockets, and long-lived SSE streams hold it until its
	// deadline, so cancel the shared base context only now to unstick both,
	// then join every handler before closing the database or other
	// dependencies they may still use.
	cancelHTTPHandlers()
	httpHandlersDone := make(chan struct{})
	go func() {
		activeHTTPHandlers.Wait()
		close(httpHandlersDone)
	}()
	// Every drain below is bounded: a handler blocked writing to a dead client
	// or a worker stuck in a non-cancellable call must not turn SIGTERM into a
	// process that never exits. After the cap, log and proceed — the remaining
	// goroutines may see errors from closing dependencies, but the process is
	// exiting either way.
	const drainGiveUp = 60 * time.Second
	select {
	case <-httpHandlersDone:
	case <-time.After(10 * time.Second):
		logger.Warn("HTTP handlers still draining after shutdown timeout")
		select {
		case <-httpHandlersDone:
		case <-time.After(drainGiveUp):
			logger.Error("giving up on HTTP handler drain; a handler appears stuck")
		}
	}

	jobShutdownCtx, cancelJobShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	if err := manager.Shutdown(jobShutdownCtx); err != nil {
		logger.Warn("job shutdown timed out; waiting before closing dependencies", "error", err)
		jobWaitCtx, cancelJobWait := context.WithTimeout(context.Background(), drainGiveUp)
		if err := manager.Wait(jobWaitCtx); err != nil {
			logger.Error("giving up on job workers; proceeding with shutdown", "error", err)
		}
		cancelJobWait()
	}
	cancelJobShutdown()

	hookShutdownCtx, cancelHookShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	if err := hookDispatcher.Shutdown(hookShutdownCtx); err != nil {
		logger.Warn("hook shutdown timed out; waiting before closing dependencies", "error", err)
		hookWaitCtx, cancelHookWait := context.WithTimeout(context.Background(), drainGiveUp)
		if err := hookDispatcher.Wait(hookWaitCtx); err != nil {
			logger.Error("giving up on hook drain; proceeding with shutdown", "error", err)
		}
		cancelHookWait()
	}
	cancelHookShutdown()
}

// recoveryTempOverride applies the same filesystem trust boundary used when
// accepting and running a job. Persisted rows may predate that validation, so
// startup cleanup must not trust their raw temp_dir values.
func recoveryTempOverride(base config.Config, job domain.Job) (string, bool, error) {
	if job.Overrides == nil || job.Overrides.TempDir == nil || *job.Overrides.TempDir == "" {
		return "", false, nil
	}
	effective, err := job.Overrides.ApplyValidated(base)
	if err != nil {
		return "", false, err
	}
	return effective.Download.TempDir, true, nil
}
