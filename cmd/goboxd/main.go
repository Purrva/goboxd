package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/thesouldev/goboxd/internal/api"
	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/logger"
	"github.com/thesouldev/goboxd/internal/sandbox"
	"github.com/thesouldev/goboxd/internal/worker"
)

// Build-time variables injected by the linker.
var (
	version   = "0.1.0"
	commit    = "dev"
	goVersion = runtime.Version()
)

func main() {
	log := logger.New()

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		log.Error("invalid config", "err", err)
		os.Exit(1)
	}

	log.Info("config loaded",
		"languages", len(cfg.Languages),
		"jail_dir", cfg.JailBaseDir,
		"max_concurrent", cfg.MaxConcurrentJobs,
	)

	// Sweep orphan jail directories from any previous unclean exit.
	if err := sandbox.SweepOrphans(cfg.JailBaseDir, cfg.OrphanMaxAge); err != nil {
		log.Warn("orphan sweep failed (non-fatal)", "err", err)
	}

	pool := worker.NewPool(cfg.MaxConcurrentJobs)

	buildInfo := api.BuildInfo{
		Version:   version,
		Commit:    commit,
		GoVersion: goVersion,
	}

	srv := api.NewServer(cfg, pool, buildInfo, log)

	addr := fmt.Sprintf(":%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start serving in a goroutine so we can wait for shutdown signal.
	go func() {
		log.Info("listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutdown signal received, draining in-flight requests")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Error("shutdown error", "err", err)
	}

	pool.Wait()
	slog.Info("shutdown complete")
}
