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
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/platform"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/room"
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
	all := []provider.Provider{provider.Yandex{}, provider.Google{}, demo}
	reg, err := provider.NewRegistry(cfg.Providers, cfg.TestProviders, all...)
	if err != nil {
		return err
	}
	if err := provider.CheckCaps(cfg.DailyCaps, all...); err != nil {
		return err
	}
	q := store.New(pool)
	tree := &places.Cache{Q: q, Providers: reg.Names()}
	catalog := maps.NewCatalog(q, reg, tree)
	games := &game.Service{
		Pool: pool, Catalog: catalog, Reg: reg,
		Usage: provider.Usage{Q: q, YandexLimit: cfg.YandexDailyLimit, Caps: cfg.DailyCaps},
	}
	rooms := &room.Hub{Games: games, Pool: pool, Store: kv.Rooms{C: valkey}}
	handler := httpapi.New(httpapi.Deps{
		Config:  cfg,
		DB:      pool,
		Valkey:  valkey,
		Players: &players.Service{Pool: pool, Q: q, TTL: cfg.SessionTTL},
		Games:   games,
		Catalog: catalog,
		Places:  tree,
		Demo:    demo,
		Rooms:   rooms,
	})

	go metrics.Serve(ctx, cfg.MetricsAddr)
	go rooms.Run(ctx)

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
		slog.Info("shafaq-api listening", "addr", cfg.Addr, "providers", reg.Names(), "test_providers", cfg.TestProviders, "daily_caps", cfg.DailyCaps, "origins", cfg.WebOrigins)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	// Graceful: the rooms are saved and their pages told to reconnect (to the next process),
	// then the requests in flight finish (a guess being scored), so a deploy loses nothing.
	slog.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rooms.Shutdown(shutdown)
	return srv.Shutdown(shutdown)
}
