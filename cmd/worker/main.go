// Command shafaq-worker runs the background jobs (River): the Yandex usage warnings and the
// cleanup. One is enough; River makes sure each job runs once even with more. It also runs
// the players' Telegram bot: "sign in with Telegram", and the chat IDs for alerts (jobs.Bot).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/db"
	"github.com/hakim-134/api-geo-shafaq/internal/jobs"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/platform"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("shafaq-worker stopped", "err", err)
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
	client, err := jobs.NewClient(pool, cfg)
	if err != nil {
		return err
	}
	go metrics.Serve(ctx, cfg.MetricsAddr)
	accounts := &players.Service{Pool: pool, Q: store.New(pool), TTL: cfg.SessionTTL}
	go jobs.Telegram{Token: cfg.TelegramBotToken, Client: &http.Client{Timeout: time.Minute}}.Run(ctx, jobs.Bot{
		Confirm: accounts.ConfirmTelegram, GameURL: gameURL(cfg.WebOrigins),
	})
	if err := client.Start(ctx); err != nil {
		return err
	}
	slog.Info("shafaq-worker started")
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return client.Stop(stopCtx)
}

// gameURL is the game's address for the bot's welcome: the first web origin without a
// wildcard port.
func gameURL(origins []string) string {
	for _, o := range origins {
		if !strings.HasSuffix(o, ":*") {
			return o
		}
	}
	return "https://geo.shafaq.uz"
}
