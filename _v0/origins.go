package main

import (
	"net/http"
	"net/url"
	"strings"
)

// Origins lists the pages allowed to use the API: "https://geo.shafaq.uz", or with any port
// "http://localhost:*". The game is on another domain, so both the WebSocket and the CORS
// check go through this list.
type Origins []string

func (o Origins) allowed(origin string) bool {
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

// checkWS is the WebSocket origin check. A request without Origin is not from a browser
// page (curl, tests), so no other site can ride on a player's browser with it.
func (o Origins) checkWS(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || o.allowed(origin)
}

// cors lets the allowed pages read the API's answers (the menu reads /api/health).
func (o Origins) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && o.allowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
