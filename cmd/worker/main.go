// Command shafaq-worker runs the background jobs (River): the Yandex usage warnings and the
// cleanup. One is enough; River makes sure each job runs once even with more. It also has
// the Telegram bot answer messages with the chat's ID (jobs.Telegram.WhoAmI).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/db"
	"github.com/hakim-134/api-geo-shafaq/internal/jobs"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/platform"
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
	// the bot answers any message with the chat's ID: how the owner finds TELEGRAM_ALERT_CHAT
	go jobs.Telegram{Token: cfg.TelegramAlertToken, Client: &http.Client{Timeout: time.Minute}}.WhoAmI(ctx)
	if err := client.Start(ctx); err != nil {
		return err
	}
	slog.Info("shafaq-worker started")
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return client.Stop(stopCtx)
}
