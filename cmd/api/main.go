// Command shafaq-api is the game's HTTP API. It serves no pages; the game is the geo-shafaq
// repo, on its own domain. Settings come from the environment (see internal/config).
//
//	go run ./cmd/api        # http://localhost:8131/v1/health
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/db"
	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi"
	"github.com/hakim-134/api-geo-shafaq/internal/kv"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/platform"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("shafaq-api stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	platform.SetupLogging(cfg)
	defer platform.FlushErrors()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	valkey, err := kv.Open(ctx, cfg.ValkeyURL)
	if err != nil {
		return err
	}
	defer valkey.Close()

	demo := provider.Demo{Secret: []byte(cfg.DemoSecret), BaseURL: cfg.PublicURL}
	reg, err := provider.NewRegistry(cfg.Providers, cfg.TestProviders, provider.Yandex{}, provider.Google{}, demo)
	if err != nil {
		return err
	}
	q := store.New(pool)
	catalog := maps.NewCatalog(q, reg)
	handler := httpapi.New(httpapi.Deps{
		Config:  cfg,
		DB:      pool,
		Valkey:  valkey,
		Players: &players.Service{Q: q, TTL: cfg.SessionTTL},
		Games: &game.Service{
			Pool: pool, Catalog: catalog, Reg: reg,
			Usage: provider.Usage{Q: q, YandexLimit: cfg.YandexDailyLimit},
		},
		Catalog: catalog,
		Demo:    demo,
	})

	go metrics.Serve(ctx, cfg.MetricsAddr)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("shafaq-api listening", "addr", cfg.Addr, "providers", reg.Names(), "test_providers", cfg.TestProviders, "origins", cfg.WebOrigins)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	// Graceful: finish the requests in flight (a guess being scored) before exiting, so a
	// deploy does not lose answers.
	slog.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
