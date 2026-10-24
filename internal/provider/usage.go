package provider

import (
	"context"
	"log/slog"
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
type Usage struct {
	Q           *store.Queries
	YandexLimit int
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
