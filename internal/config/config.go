// Package config reads the settings from the environment (direnv's .envrc locally, the
// compose file on the server). Secrets are only ever read here, never logged.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr        string   // API listen address
	MetricsAddr string   // Prometheus /metrics, kept off the public port
	DatabaseURL string   //
	ValkeyURL   string   //
	WebOrigins  []string // pages allowed to call the API; a port may be * (http://localhost:*)
	PublicURL   string   // how browsers reach this API, for links it hands out
	TrustProxy  bool     // behind our own Caddy/nginx: the client is in X-Forwarded-For

	CookieSecure bool
	CookieDomain string // empty = the API's host only
	SessionTTL   time.Duration

	Providers        []string // enabled panorama providers, in order of preference
	DemoSecret       string   // signs the demo provider's opaque references
	YandexDailyLimit int      // the free key's daily panorama views
	MapillaryToken   string   // server-side token for the pipeline and the audit

	TelegramAlertToken string // bot token for warnings to the owner (optional)
	TelegramAlertChat  string

	LogFormat string // text | json
	LogLevel  string
	Version   string
}

func Load() (Config, error) {
	c := Config{
		Addr:               env("ADDR", ":8131"),
		MetricsAddr:        env("METRICS_ADDR", "127.0.0.1:9131"),
		DatabaseURL:        env("DATABASE_URL", "postgres://shafaq:shafaq@127.0.0.1:5433/shafaq?sslmode=disable"),
		ValkeyURL:          env("VALKEY_URL", "redis://127.0.0.1:6380/0"),
		WebOrigins:         list(env("WEB_ORIGINS", "https://geo.shafaq.uz,http://localhost:*,http://127.0.0.1:*")),
		PublicURL:          strings.TrimRight(env("PUBLIC_URL", "http://localhost:8131"), "/"),
		TrustProxy:         boolean(env("TRUST_PROXY", "false")),
		CookieSecure:       boolean(env("COOKIE_SECURE", "false")),
		CookieDomain:       env("COOKIE_DOMAIN", ""),
		SessionTTL:         365 * 24 * time.Hour,
		Providers:          list(env("PROVIDERS", "yandex,mapillary,demo")),
		DemoSecret:         env("DEMO_SECRET", "dev-only-demo-secret"),
		YandexDailyLimit:   integer(env("YANDEX_DAILY_LIMIT", "100")),
		MapillaryToken:     env("MAPILLARY_TOKEN", ""),
		TelegramAlertToken: env("TELEGRAM_ALERT_TOKEN", ""),
		TelegramAlertChat:  env("TELEGRAM_ALERT_CHAT", ""),
		LogFormat:          env("LOG_FORMAT", "text"),
		LogLevel:           env("LOG_LEVEL", "info"),
		Version:            env("VERSION", "dev"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is empty")
	}
	if len(c.Providers) == 0 {
		return c, fmt.Errorf("PROVIDERS is empty")
	}
	return c, nil
}

// env reads a setting; unset or empty means the default (compose passes unset ones as "").
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func list(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func boolean(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}

func integer(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
