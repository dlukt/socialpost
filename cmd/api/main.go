package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/dlukt/socialpost/internal/httpserver"
	"github.com/dlukt/socialpost/internal/platform/config"
	"github.com/dlukt/socialpost/internal/platform/db"
	"github.com/dlukt/socialpost/internal/platform/logging"
	"github.com/dlukt/socialpost/internal/platform/queue"
	appRuntime "github.com/dlukt/socialpost/internal/runtime"
)

func main() {
	ctx, stop := appRuntime.ContextWithSignals(context.Background())
	defer stop()

	cfg, err := config.Load("api")
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.LogLevel).With(
		"service", cfg.ServiceName,
		"env", cfg.Environment,
	)

	postgresPool, err := db.Open(ctx, cfg.PostgresURL)
	if err != nil {
		logger.Error("failed to connect postgres", "error", err)
		os.Exit(1)
	}
	defer postgresPool.Close()

	redisClient, err := queue.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("failed to connect redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	srv := httpserver.New(cfg.APIListenAddr, cfg.ServiceName, httpserver.Dependencies{
		Postgres: postgresPool,
		Redis:    redisClient,
	})

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api server starting", "addr", cfg.APIListenAddr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("api server failed", "error", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("api shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("api stopped")
}
