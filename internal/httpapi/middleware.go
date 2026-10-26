package httpapi

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Origins lists the pages allowed to use the API: "https://geo.shafaq.uz", or with any port
// "http://localhost:*". The game is on another domain, so CORS and the write check go
// through this list.
type Origins []string

func (o Origins) Allowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	for _, pattern := range o {
		if base, ok := strings.CutSuffix(pattern, ":*"); ok {
			if u.Scheme+"://"+u.Hostname() == base {
				return true
			}
		} else if origin == pattern {
			return true
		}
	}
	return false
}

// cors lets the allowed pages call the API with their session cookie.
func (o Origins) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin != "" && o.Allowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// guardWrites is the CSRF protection: a browser always sends Origin with a POST, so a
// POST from a page that is not ours is refused. The session cookie is SameSite=Lax too.
func (o Origins) guardWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && !o.Allowed(origin) {
				writeError(w, http.StatusForbidden, "bad_origin")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack lets a room's WebSocket take the connection over.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack not supported")
	}
	w.status = http.StatusSwitchingProtocols
	return h.Hijack()
}

type routeKey struct{}

// recordRoute sits right around the router and tells observe which route matched (the
// router sets it on the request it is given, which observe does not see).
func recordRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if p, ok := r.Context().Value(routeKey{}).(*string); ok {
			*p = r.Pattern
		}
	})
}

// observe logs each request and records its metrics, by route pattern (not by URL, so a
// game id does not become a new time series).
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		var route string
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, &route))
		defer func() {
			if p := recover(); p != nil {
				slog.Error("panic", "err", p, "stack", string(debug.Stack()), "path", r.URL.Path)
				if sw.status == http.StatusOK {
					writeError(sw, http.StatusInternalServerError, "internal")
				}
			}
			if route == "" {
				route = "unmatched"
			}
			took := time.Since(start)
			metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
			if sw.status != http.StatusSwitchingProtocols { // a WebSocket's time is its whole life
				metrics.HTTPDuration.WithLabelValues(route).Observe(took.Seconds())
			}
			level := slog.LevelDebug
			if sw.status >= 500 {
				level = slog.LevelError
			}
			slog.Log(r.Context(), level, "request", "method", r.Method, "route", route, "status", sw.status, "took", took)
		}()
		next.ServeHTTP(sw, r)
	})
}

type userKey struct{}

// withUser puts the session's player (if any) into the request context.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(s.cookieName); err == nil {
			u, err := s.players.FromToken(r.Context(), c.Value)
			if err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, &u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey{}).(*store.User)
	return u
}

// clientIP is the player's address. Behind our own proxy (TRUST_PROXY) it is the last
// address the proxy added to X-Forwarded-For; otherwise the connection's.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
