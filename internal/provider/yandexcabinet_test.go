package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// fakeCabinet answers like the Developer's Account API: one project, one service, two counters.
func fakeCabinet(t *testing.T, used int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Key") != "k" {
			http.Error(w, "no key", http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"projects":[{"id":"p1","name":"Project #1"}]}`))
	})
	mux.HandleFunc("GET /projects/p1/services", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"services":[{"id":"s1","name":"JavaScript API"}]}`))
	})
	mux.HandleFunc("GET /projects/p1/services/s1/limits", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"limits":{
			"panorama_daily":{"limit":100,"reset_date":"2026-10-24T21:00:00","value":` + strconv.Itoa(used) + `},
			"other_daily":{"limit":-1,"reset_date":"2026-10-24T21:00:00","value":7}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestYandexCabinetQuotas(t *testing.T) {
	srv := fakeCabinet(t, 85)
	c := YandexCabinet{Key: "k", Base: srv.URL, Client: srv.Client()}
	qs, err := c.Quotas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d quotas, want 2: %+v", len(qs), qs)
	}
	other, pano := qs[0], qs[1] // sorted by name
	if pano.Name != "panorama_daily" || pano.Service != "JavaScript API" || pano.Used != 85 || pano.Limit != 100 {
		t.Fatalf("panorama quota %+v", pano)
	}
	if !pano.Near() {
		t.Fatal("85 of 100 is past 80%")
	}
	if other.Near() {
		t.Fatal("a counter without a limit is never near it")
	}

	bad := YandexCabinet{Key: "wrong", Base: srv.URL, Client: srv.Client()}
	if _, err := bad.Quotas(context.Background()); err == nil {
		t.Fatal("a refused key must be an error")
	}
}
