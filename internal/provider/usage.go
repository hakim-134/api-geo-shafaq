package provider

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// LimitClock is the clock Yandex's daily limits reset by: midnight in Moscow (21:00 UTC).
var LimitClock = time.FixedZone("MSK", 3*60*60)

// Today is the day a view at now counts for, as stored in provider_usage.
func Today(now time.Time) time.Time {
	d := now.In(LimitClock)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

// WarnShare of a daily limit is when the owner hears about it.
const WarnShare = 0.8

// Usage counts panorama views per provider and day. Yandex's free key allows about 100 a
// day; going over again and again gets the key blocked for good, so the license is bought
// when the count gets close (the worker warns at 80%). It counts rounds, not every request
// the browser makes; Yandex's own count comes from YandexCabinet.
//
// Caps are daily budgets in rounds (DAILY_CAPS): a provider that reached its cap gets no new
// rounds that day, so a paid provider never spends more than planned, and the game uses its
// other providers instead.
type Usage struct {
	Q           *store.Queries
	YandexLimit int
	Caps        map[string]int
}

// CheckCaps refuses a cap for a provider that does not exist: a typo would leave a paid
// provider without its budget.
func CheckCaps(caps map[string]int, all ...Provider) error {
	for name := range caps {
		if !slices.ContainsFunc(all, func(p Provider) bool { return p.Name() == name }) {
			return fmt.Errorf("DAILY_CAPS: unknown panorama provider %q", name)
		}
		metrics.PanoramaCap.WithLabelValues(name).Set(float64(caps[name]))
	}
	return nil
}

// Capped are the providers that reached their cap today.
func (u Usage) Capped(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if len(u.Caps) == 0 {
		return out, nil
	}
	rows, err := u.Q.UsageOn(ctx, Today(time.Now()))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if limit, ok := u.Caps[r.Provider]; ok && limit > 0 && int(r.Views) >= limit {
			out[r.Provider] = true
		}
	}
	return out, nil
}

// Count records one view. It never fails the round: a counting error is only logged.
func (u Usage) Count(ctx context.Context, provider string) {
	metrics.PanoramaViews.WithLabelValues(provider).Inc()
	views, err := u.Q.CountProviderView(ctx, store.CountProviderViewParams{Day: Today(time.Now()), Provider: provider})
	if err != nil {
		slog.Error("counting a panorama view", "provider", provider, "err", err)
		return
	}
	metrics.PanoramaViewsToday.WithLabelValues(provider).Set(float64(views))
	if provider == "yandex" && u.YandexLimit > 0 && int(views) == int(float64(u.YandexLimit)*WarnShare) {
		slog.Warn("yandex views near the free daily limit", "views", views, "limit", u.YandexLimit)
	}
}
