// Package jobs is the background work, run by River (a Postgres job queue) in the worker.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// UsageCheckArgs: warn the owner when Yandex views reach 80% of the free daily limit.
type UsageCheckArgs struct{}

func (UsageCheckArgs) Kind() string { return "usage_check" }

type UsageCheck struct {
	river.WorkerDefaults[UsageCheckArgs]
	Q     *store.Queries
	Limit int
	Alert Alerter
}

func (w *UsageCheck) Work(ctx context.Context, _ *river.Job[UsageCheckArgs]) error {
	if w.Limit <= 0 {
		return nil
	}
	threshold := int32(float64(w.Limit) * provider.WarnShare)
	rows, err := w.Q.UsageToWarn(ctx, store.UsageToWarnParams{Provider: "yandex", Threshold: threshold})
	if err != nil {
		return err
	}
	for _, r := range rows {
		msg := fmt.Sprintf("Shafaq Geo: %d Yandex panorama views on %s, the free key allows about %d a day. "+
			"Going over again and again gets the key blocked for good: time to buy the commercial license.",
			r.Views, r.Day.Format("2006-01-02"), w.Limit)
		slog.Warn("yandex usage warning", "day", r.Day, "views", r.Views, "limit", w.Limit)
		if err := w.Alert.Send(ctx, msg); err != nil {
			return err // retried by River; the day stays unwarned until it works
		}
		if err := w.Q.MarkUsageWarned(ctx, store.MarkUsageWarnedParams{Day: r.Day, Provider: r.Provider}); err != nil {
			return err
		}
	}
	return nil
}

// CleanupArgs: drop expired sessions, mark games nobody finished as abandoned.
type CleanupArgs struct{}

func (CleanupArgs) Kind() string { return "cleanup" }

type Cleanup struct {
	river.WorkerDefaults[CleanupArgs]
	Q *store.Queries
}

func (w *Cleanup) Work(ctx context.Context, _ *river.Job[CleanupArgs]) error {
	sessions, err := w.Q.DeleteExpiredSessions(ctx)
	if err != nil {
		return err
	}
	games, err := w.Q.AbandonStaleGames(ctx, 6)
	if err != nil {
		return err
	}
	slog.Info("cleanup", "expired_sessions", sessions, "abandoned_games", games)
	return nil
}

// Alerter sends a message to the owner. Without a Telegram bot configured it only logs.
type Alerter struct {
	Token, Chat string
	Client      *http.Client
}

func (a Alerter) Send(ctx context.Context, text string) error {
	if a.Token == "" || a.Chat == "" {
		return nil
	}
	form := url.Values{"chat_id": {a.Chat}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+a.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", err) // the token is in the URL; never log the request
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: %s", res.Status)
	}
	return nil
}

// NewClient builds the River client with the workers and their schedules.
func NewClient(pool *pgxpool.Pool, cfg config.Config) (*river.Client[pgx.Tx], error) {
	q := store.New(pool)
	workers := river.NewWorkers()
	river.AddWorker(workers, &UsageCheck{Q: q, Limit: cfg.YandexDailyLimit, Alert: Alerter{
		Token: cfg.TelegramAlertToken, Chat: cfg.TelegramAlertChat, Client: &http.Client{Timeout: 10 * time.Second},
	}})
	river.AddWorker(workers, &Cleanup{Q: q})
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return UsageCheckArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return CleanupArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		},
	})
}
