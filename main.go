// Command api-geo-shafaq runs the live rooms for playing with friends. API only: the game
// pages are the geo-shafaq repo, served on their own domain.
//
//	go run .                                      # http://localhost:8131
//	go run . -origins 'http://10.42.0.1:*'        # also let a page on that LAN address in
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// The site that may use this API. The domain is temporary; change it here.
const webOrigin = "https://geo.shafaq.uz"

// Local runs: the frontend dev server on any port of this machine.
var devOrigins = []string{"http://localhost:*", "http://127.0.0.1:*"}

func newMux(hub *Hub, origins Origins) http.Handler {
	up := &websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 4096, CheckOrigin: origins.checkWS}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.ServeWS(up))
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"ok":true,"rooms":true}`))
	})
	return origins.cors(mux)
}

func main() {
	addr := flag.String("addr", ":8131", "listen address")
	extra := flag.String("origins", "", "more allowed page origins, comma-separated; a port may be * (http://10.42.0.1:*)")
	flag.Parse()

	origins := append(Origins{webOrigin}, devOrigins...)
	origins = append(origins, splitList(*extra)...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := NewHub()
	go hub.janitor(ctx)

	srv := &http.Server{Addr: *addr, Handler: newMux(hub, origins), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("api-geo-shafaq listening on %s, pages allowed from %v", *addr, origins)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
