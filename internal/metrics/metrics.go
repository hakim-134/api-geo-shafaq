// Package metrics defines the Prometheus metrics. They are served on a separate,
// private address (METRICS_ADDR), never on the public API port.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	Registry = prometheus.NewRegistry()

	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shafaq_http_requests_total", Help: "API requests by route and status code.",
	}, []string{"route", "code"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "shafaq_http_request_seconds", Help: "API request latency by route.",
		Buckets: []float64{.005, .01, .025, .05, .1, .2, .5, 1, 2.5},
	}, []string{"route"})
	PanoramaViews = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shafaq_panorama_views_total", Help: "Panoramas handed to viewers, by provider.",
	}, []string{"provider"})
	PanoramaViewsToday = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "shafaq_panorama_views_today", Help: "Panoramas handed to viewers today (Moscow day, as Yandex counts), by provider.",
	}, []string{"provider"})
	PanoramaCap = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "shafaq_panorama_daily_cap", Help: "Rounds a provider may get per day (DAILY_CAPS; 0 = no cap), by provider.",
	}, []string{"provider"})
	YandexUsed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "shafaq_yandex_requests_today", Help: "Yandex's own count of today's requests, by service and counter.",
	}, []string{"service", "counter"})
	YandexLimit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "shafaq_yandex_daily_limit", Help: "Yandex's daily limit, by service and counter (-1 = none).",
	}, []string{"service", "counter"})
	GamesStarted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shafaq_games_started_total", Help: "Games started, by map.",
	}, []string{"map"})
	RoundsScored = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shafaq_rounds_scored_total", Help: "Rounds ended with a guess or without one.",
	})
	GuestsCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shafaq_guests_created_total", Help: "Guest players created.",
	})
	RoomsOpen = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "shafaq_rooms_open", Help: "Live rooms in this process's memory.",
	})
	RoomConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "shafaq_room_connections", Help: "Open room WebSockets.",
	})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, PanoramaViews, PanoramaViewsToday, PanoramaCap, YandexUsed, YandexLimit, GamesStarted, RoundsScored, GuestsCreated,
		RoomsOpen, RoomConnections,
	)
}

// Serve runs the /metrics endpoint until ctx ends. An empty addr turns it off.
func Serve(ctx context.Context, addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("metrics listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("metrics server", "err", err)
	}
}
