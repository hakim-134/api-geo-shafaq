package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

const metadataURL = "https://maps.googleapis.com/maps/api/streetview/metadata"

// How far from a library point a Google panorama may be. Street points must stay on their
// street (the same 120 m as Yandex on street maps); the country points are looser.
const (
	streetRadiusM = 50
	streetSnapKm  = 0.12
	pointRadiusM  = 500
)

type googleMeta struct {
	Status       string `json:"status"` // OK | ZERO_RESULTS | NOT_FOUND | REQUEST_DENIED | …
	ErrorMessage string `json:"error_message"`
	PanoID       string `json:"pano_id"`
	Date         string `json:"date"` // "2019-05"
	Copyright    string `json:"copyright"`
	Location     struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"location"`
}

// ScanGoogle looks for an outdoor Google panorama near every Yandex library point and adds
// what it finds as Google panoramas. It uses Street View metadata look-ups, which Google does
// not charge for. Running it again updates the same pano IDs.
func ScanGoogle(ctx context.Context, pool *pgxpool.Pool, key string, out io.Writer) error {
	if key == "" {
		return errors.New("GOOGLE_MAPS_KEY is empty")
	}
	q := store.New(pool)
	points, err := q.ScanPoints(ctx)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}

	// Fail fast on a bad key or a project without billing, before 3 000 look-ups.
	if _, err := lookup(ctx, client, key, points[0].Lat, points[0].Lon, pointRadiusM); err != nil {
		return err
	}

	type result struct {
		pt   store.ScanPointsRow
		meta *googleMeta
		err  error
	}
	jobs := make(chan store.ScanPointsRow)
	results := make(chan result)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pt := range jobs {
				radius := pointRadiusM
				if pt.StreetID != nil {
					radius = streetRadiusM
				}
				m, err := lookup(ctx, client, key, pt.Lat, pt.Lon, radius)
				results <- result{pt, m, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, pt := range points {
			select {
			case jobs <- pt:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	type tally struct{ checked, found, official int }
	bySource := map[string]*tally{}
	seen := map[string]bool{}
	failed := 0
	for r := range results {
		t := bySource[r.pt.Source]
		if t == nil {
			t = &tally{}
			bySource[r.pt.Source] = t
		}
		t.checked++
		if r.err != nil {
			failed++
			slog.Warn("google look-up failed", "point", r.pt.ID, "err", r.err)
			continue
		}
		if r.meta == nil {
			continue
		}
		pos := geo.Point{Lat: r.meta.Location.Lat, Lon: r.meta.Location.Lng}
		streetID := r.pt.StreetID
		if streetID != nil && geo.DistanceKm(pos, geo.Point{Lat: r.pt.Lat, Lon: r.pt.Lon}) > streetSnapKm {
			continue // not on this street
		}
		if seen[r.meta.PanoID] {
			continue // two library points found the same panorama
		}
		seen[r.meta.PanoID] = true
		t.found++
		if r.meta.Copyright == "© Google" {
			t.official++ // Google's own cars or trekkers, not a user's photosphere
		}
		err := q.UpsertPanorama(ctx, store.UpsertPanoramaParams{
			Provider: "google", Ref: r.meta.PanoID, Lat: pos.Lat, Lon: pos.Lon,
			TakenAt: month(r.meta.Date), StreetID: streetID, Source: "google_scan",
		})
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	fmt.Fprintf(out, "%-14s %8s %8s %10s\n", "library points", "checked", "google", "by Google")
	for src, t := range bySource {
		fmt.Fprintf(out, "%-14s %8d %8d %10d\n", src, t.checked, t.found, t.official)
	}
	if failed > 0 {
		fmt.Fprintf(out, "%d look-ups failed (see the log); run again to retry them\n", failed)
	}
	fmt.Fprintln(out, "'by Google' = Google's own imagery; the rest are photospheres people uploaded.")
	return Tag(ctx, q, false)
}

// lookup returns the nearest outdoor panorama within radius metres, or nil if there is none.
func lookup(ctx context.Context, c *http.Client, key string, lat, lon float64, radius int) (*googleMeta, error) {
	v := url.Values{
		"location": {strconv.FormatFloat(lat, 'f', 6, 64) + "," + strconv.FormatFloat(lon, 'f', 6, 64)},
		"radius":   {strconv.Itoa(radius)},
		"source":   {"outdoor"},
		"key":      {key},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds the key; keep it out of logs
		}
		return nil, err
	}
	defer res.Body.Close()
	var m googleMeta
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("metadata: HTTP %d: %w", res.StatusCode, err)
	}
	switch m.Status {
	case "OK":
		return &m, nil
	case "ZERO_RESULTS", "NOT_FOUND":
		return nil, nil
	}
	return nil, fmt.Errorf("metadata: %s: %s", m.Status, m.ErrorMessage)
}

// month turns Google's "2019-05" into the first day of that month.
func month(s string) *time.Time {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return nil
	}
	return &t
}
