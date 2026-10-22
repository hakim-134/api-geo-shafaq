// Package platform is the plumbing every binary shares: logging, the metrics server and
// graceful shutdown.
package platform

import (
	"log/slog"
	"os"
	"strings"

	"github.com/hakim-134/api-geo-shafaq/internal/config"
)

// SetupLogging makes slog the default logger: text for people, JSON for Loki on the server.
func SetupLogging(cfg config.Config) {
	var level slog.Level
	_ = level.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel)))
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h).With("version", cfg.Version))
}
