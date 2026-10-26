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
	DatabaseURL string   // without a password, pgx takes PGPASSWORD
	ValkeyURL   string   //
	WebOrigins  []string // pages allowed to call the API; a port may be * (http://localhost:*)
	PublicURL   string   // how browsers reach this API, for links it hands out; empty = relative links
	TrustProxy  bool     // behind our own Caddy/nginx: the client is in X-Forwarded-For

	CookieSecure bool
	CookieDomain string // empty = the API's host only
	SessionTTL   time.Duration

	Providers        []string // the players' panorama providers, in order of preference
	TestProviders    []string // enabled only for games that name them (demo: development, smoke tests)
	DemoSecret       string   // signs the demo provider's opaque references
	YandexDailyLimit int      // the free key's daily panorama views
	YandexCabinetKey string   // Developer's Account API: Yandex's own request counts (optional)
	// DailyCaps: rounds a provider may get per day (Moscow time), then the game uses its other
	// providers. "google=160,yandex=900"; a provider not named has no cap. Google's default
	// keeps it inside its free 5 000 panoramas a month.
	DailyCaps     map[string]int
	GoogleMapsKey string // server-side: `shafaq-tools google` (free metadata look-ups)

	TelegramAlertToken string // bot token for warnings to the owner (optional)
	TelegramAlertChat  string

	LogFormat    string // text | json
	LogLevel     string
	Version      string
	Environment  string // local | dev | prod: tags error reports
	GlitchtipDSN string // error tracking (GlitchTip, Sentry protocol); empty = off
}

func Load() (Config, error) {
	c := Config{
		Addr:               env("ADDR", ":8131"),
		MetricsAddr:        env("METRICS_ADDR", "127.0.0.1:9131"),
		DatabaseURL:        env("DATABASE_URL", "postgres://postgres@127.0.0.1:5432/shafaq_lcl?sslmode=disable"),
		ValkeyURL:          env("VALKEY_URL", "redis://127.0.0.1:6379/1"),
		WebOrigins:         list(env("WEB_ORIGINS", "https://geo.shafaq.uz,http://localhost:*,http://127.0.0.1:*")),
		PublicURL:          strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		TrustProxy:         boolean(env("TRUST_PROXY", "false")),
		CookieSecure:       boolean(env("COOKIE_SECURE", "false")),
		CookieDomain:       env("COOKIE_DOMAIN", ""),
		SessionTTL:         365 * 24 * time.Hour,
		Providers:          list(env("PROVIDERS", "yandex")),
		TestProviders:      list(env("TEST_PROVIDERS", "")),
		DemoSecret:         env("DEMO_SECRET", "dev-only-demo-secret"),
		YandexDailyLimit:   integer(env("YANDEX_DAILY_LIMIT", "100")),
		YandexCabinetKey:   env("YANDEX_CABINET_KEY", ""),
		GoogleMapsKey:      env("GOOGLE_MAPS_KEY", ""),
		TelegramAlertToken: env("TELEGRAM_ALERT_TOKEN", ""),
		TelegramAlertChat:  env("TELEGRAM_ALERT_CHAT", ""),
		LogFormat:          env("LOG_FORMAT", "text"),
		LogLevel:           env("LOG_LEVEL", "info"),
		Version:            env("VERSION", "dev"),
		Environment:        env("ENVIRONMENT", "local"),
		GlitchtipDSN:       env("GLITCHTIP_DSN", ""),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is empty")
	}
	if len(c.Providers) == 0 {
		return c, fmt.Errorf("PROVIDERS is empty")
	}
	var err error
	if c.DailyCaps, err = caps(env("DAILY_CAPS", "google=160")); err != nil {
		return c, err
	}
	return c, nil
}

// caps reads "name=rounds,…"; 0 = no cap.
func caps(s string) (map[string]int, error) {
	out := map[string]int{}
	for _, part := range list(s) {
		name, n, ok := strings.Cut(part, "=")
		rounds, err := strconv.Atoi(strings.TrimSpace(n))
		if !ok || err != nil || rounds < 0 {
			return nil, fmt.Errorf("DAILY_CAPS: %q is not provider=rounds", part)
		}
		out[strings.TrimSpace(name)] = rounds
	}
	return out, nil
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
