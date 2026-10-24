// Command shafaq-tools sets up and maintains the data.
//
//	shafaq-tools migrate                      schema up to date (goose + River)
//	shafaq-tools places -pbf uzbekistan.osm.pbf   places from an OSM extract
//	shafaq-tools v0                           v0's panorama pools into the library
//	shafaq-tools demo                         a demo twin of every Yandex panorama
//	shafaq-tools maps                         the official maps
//	shafaq-tools tag                          re-tag every panorama with its places
//	shafaq-tools stats                        what is in the library
//	shafaq-tools google                       Google panoramas near the library points (GOOGLE_MAPS_KEY)
//	shafaq-tools setup -pbf …                 all of the above, in order, on an empty database
//
// Settings come from the environment, like the API's.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/db"
	"github.com/hakim-134/api-geo-shafaq/internal/library"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/platform"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: shafaq-tools <migrate|places|v0|demo|maps|tag|stats|google|setup> [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	pbf := fs.String("pbf", "", "OpenStreetMap extract (.osm.pbf), e.g. from download.geofabrik.de/asia/uzbekistan-latest.osm.pbf")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	platform.SetupLogging(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	if cmd == "google" {
		err = library.ScanGoogle(ctx, pool, cfg.GoogleMapsKey, os.Stdout)
	} else {
		err = run(ctx, pool, cmd, *pbf)
	}
	if err != nil {
		fatal(err)
	}
}

func run(ctx context.Context, pool *pgxpool.Pool, cmd, pbf string) error {
	q := store.New(pool)
	switch cmd {
	case "migrate":
		return db.Migrate(ctx, pool)
	case "places":
		if pbf == "" {
			return errors.New("-pbf is required")
		}
		return places.Import(ctx, pool, pbf)
	case "v0":
		return library.ImportV0(ctx, pool)
	case "demo":
		return library.CopyDemo(ctx, pool)
	case "maps":
		return maps.SeedOfficial(ctx, pool)
	case "tag":
		return library.Tag(ctx, q, true)
	case "stats":
		return stats(ctx, q)
	case "setup":
		for _, step := range []string{"migrate", "places", "v0", "demo", "maps", "stats"} {
			slog.Info("setup", "step", step)
			if err := run(ctx, pool, step, pbf); err != nil {
				return fmt.Errorf("%s: %w", step, err)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func stats(ctx context.Context, q *store.Queries) error {
	pl, err := q.CountPlaces(ctx)
	if err != nil {
		return err
	}
	for _, p := range pl {
		fmt.Printf("places   %-10s %6d\n", p.Kind, p.N)
	}
	pa, err := q.CountPanoramas(ctx)
	if err != nil {
		return err
	}
	for _, p := range pa {
		fmt.Printf("panorama %-10s %-13s %-9s %6d\n", p.Provider, p.Source, p.Status, p.N)
	}
	return nil
}

func fatal(err error) {
	slog.Error("shafaq-tools failed", "err", err)
	os.Exit(1)
}
