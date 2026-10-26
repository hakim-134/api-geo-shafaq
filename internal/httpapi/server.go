// Package httpapi is the HTTP side of the API. The routes and types come from
// api/openapi.yaml (oapi-codegen, package oapi); this package fills them in.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"github.com/hakim-134/api-geo-shafaq/internal/config"
	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/kv"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

type Server struct {
	db      *pgxpool.Pool
	valkey  *redis.Client
	q       *store.Queries
	players *players.Service
	games   *game.Service
	catalog *maps.Catalog
	places  *places.Cache
	demo    provider.Demo
	limit   kv.Limiter
	version string
	bot     *telegramBot
	google  *auth.Google

	cookieName   string
	cookieSecure bool
	cookieDomain string
	sessionTTL   time.Duration
	trustProxy   bool
}

type Deps struct {
	Config  config.Config
	DB      *pgxpool.Pool
	Valkey  *redis.Client
	Players *players.Service
	Games   *game.Service
	Catalog *maps.Catalog
	Places  *places.Cache
	Demo    provider.Demo
}

// New builds the API's handler: the generated routes, wrapped in the middleware.
func New(d Deps) http.Handler {
	s := &Server{
		db: d.DB, valkey: d.Valkey, q: store.New(d.DB), players: d.Players, games: d.Games,
		catalog: d.Catalog, places: d.Places, demo: d.Demo, limit: kv.Limiter{C: d.Valkey}, version: d.Config.Version,
		cookieName: "sg_session", cookieSecure: d.Config.CookieSecure, cookieDomain: d.Config.CookieDomain,
		sessionTTL: d.Config.SessionTTL, trustProxy: d.Config.TrustProxy,
		bot:    &telegramBot{token: d.Config.TelegramBotToken, client: &http.Client{Timeout: 10 * time.Second}},
		google: &auth.Google{ClientID: d.Config.GoogleClientID},
	}
	origins := Origins(d.Config.WebOrigins)
	mux := http.NewServeMux()
	oapi.HandlerWithOptions(s, oapi.StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_input") // a malformed path or query
		},
	})
	var h http.Handler = recordRoute(mux)
	h = s.withUser(h)
	h = origins.guardWrites(h)
	h = origins.cors(h)
	h = observe(h)
	return http.MaxBytesHandler(h, 64<<10)
}

// ---------- responses ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, oapi.Error{Code: code})
}

// fail maps the services' errors to HTTP answers with their stable codes.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal"
	switch {
	case errors.Is(err, game.ErrNotFound), errors.Is(err, maps.ErrNotFound), errors.Is(err, players.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, game.ErrBadInput), errors.Is(err, players.ErrBadAvatar), errors.Is(err, players.ErrBadPrefs):
		status, code = http.StatusBadRequest, "bad_input"
	case errors.Is(err, players.ErrBadName):
		status, code = http.StatusBadRequest, "bad_name"
	case errors.Is(err, players.ErrLastIdentity):
		status, code = http.StatusConflict, "last_identity"
	case errors.Is(err, auth.ErrBadLogin):
		status, code = http.StatusUnauthorized, "bad_login"
	case errors.Is(err, game.ErrWrongPhase), errors.Is(err, game.ErrGameOver), errors.Is(err, game.ErrTooFar),
		errors.Is(err, game.ErrNoHints), errors.Is(err, game.ErrTooManyFails):
		status, code = http.StatusConflict, err.Error()
	case errors.Is(err, game.ErrNoPanorama), errors.Is(err, game.ErrDailyCap):
		status, code = http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, context.Canceled):
		return // the player went away
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "route", r.Pattern, "err", err)
	}
	writeError(w, status, code)
}

func decode(r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

// player returns the session's user, or answers 401.
func (s *Server) player(w http.ResponseWriter, r *http.Request) *store.User {
	u := userFrom(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "no_session")
	}
	return u
}

// allow applies a rate limit, answering 429 when it is used up.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	if s.limit.Allow(r.Context(), key, limit, window) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited")
	return false
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: token, Path: "/", Domain: s.cookieDomain,
		MaxAge: int(s.sessionTTL.Seconds()), HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

var _ oapi.ServerInterface = (*Server)(nil)
