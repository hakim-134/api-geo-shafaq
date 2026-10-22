package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Tashkent is the clock the daily limits are counted in.
var Tashkent = time.FixedZone("Asia/Tashkent", 5*60*60)

// WarnShare of a daily limit is when the owner hears about it.
const WarnShare = 0.8

// Usage counts panorama views per provider and day. Yandex's free key allows about 100 a
// day; going over again and again gets the key blocked for good, so the license is bought
// when the count gets close (the worker warns at 80%).
type Usage struct {
	Q           *store.Queries
	YandexLimit int
}

// Count records one view. It never fails the round: a counting error is only logged.
func (u Usage) Count(ctx context.Context, provider string) {
	metrics.PanoramaViews.WithLabelValues(provider).Inc()
	day := time.Now().In(Tashkent)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	views, err := u.Q.CountProviderView(ctx, store.CountProviderViewParams{Day: day, Provider: provider})
	if err != nil {
		slog.Error("counting a panorama view", "provider", provider, "err", err)
		return
	}
	metrics.PanoramaViewsToday.WithLabelValues(provider).Set(float64(views))
	if provider == "yandex" && u.YandexLimit > 0 && int(views) == int(float64(u.YandexLimit)*WarnShare) {
		slog.Warn("yandex views near the free daily limit", "views", views, "limit", u.YandexLimit)
	}
}
