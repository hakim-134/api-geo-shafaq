// Package library fills the panorama library and the official maps.
package library

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/data"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

type v0Locations struct {
	Points [][2]float64 `json:"points"` // [lat, lon]
}

type v0Tashkent struct {
	Streets []struct {
		Uz     string       `json:"uz"`
		Ru     string       `json:"ru"`
		Points [][2]float64 `json:"points"`
	} `json:"streets"`
	Metro []struct {
		Uz  string  `json:"uz"`
		Ru  string  `json:"ru"`
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"metro"`
}

// ruTypes go first in a Russian street name, as Tashkent says it and Yandex's map writes it:
// "улица Фурката", "проспект Амира Тимура". v0's list puts them last ("Фурката улица"). An
// adjective keeps the type last: "Чиланзарская улица", "Ферганское шоссе".
var (
	ruTypes     = []string{"улица", "проспект", "переулок", "проезд", "тупик", "шоссе", "бульвар", "площадь", "дорога", "набережная"}
	ruAdjective = regexp.MustCompile(`(ск|цк|н|в)(ая|ое|ий|ый|ой)$`)
)

func ruStreet(s string) string {
	w := strings.Fields(s)
	if len(w) < 2 || !slices.Contains(ruTypes, w[len(w)-1]) || ruAdjective.MatchString(w[len(w)-2]) {
		return s
	}
	return w[len(w)-1] + " " + strings.Join(w[:len(w)-1], " ")
}

// YandexRef is how a Yandex panorama is keyed in the library: the point, to 5 decimals
// (about a metre). The JS API finds panoramas by position, not by id.
func YandexRef(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', 5, 64) + "," + strconv.FormatFloat(lon, 'f', 5, 64)
}

// ImportV0 puts v0's pools into the library as Yandex panoramas: the 680 points all over the
// country, and Tashkent's 177 main streets with their points and the metro stations. Places
// must be imported first; they name the city the streets belong to.
func ImportV0(ctx context.Context, pool *pgxpool.Pool) error {
	var locs v0Locations
	var tk v0Tashkent
	if err := readJSON("v0/locations.json", &locs); err != nil {
		return err
	}
	if err := readJSON("v0/tashkent.json", &tk); err != nil {
		return err
	}

	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		tashkent, err := q.PlaceBySlug(ctx, "toshkent")
		if err != nil {
			return fmt.Errorf("place 'toshkent' (import places first): %w", err)
		}
		for _, p := range locs.Points {
			err := q.UpsertPanorama(ctx, store.UpsertPanoramaParams{
				Provider: "yandex", Ref: YandexRef(p[0], p[1]), Lat: p[0], Lon: p[1], Source: "v0_locations",
			})
			if err != nil {
				return err
			}
		}
		n := 0
		for _, s := range tk.Streets {
			streetID, err := q.UpsertStreet(ctx, store.UpsertStreetParams{PlaceID: tashkent.ID, NameUz: s.Uz, NameRu: ruStreet(s.Ru)})
			if err != nil {
				return err
			}
			for _, p := range s.Points {
				err := q.UpsertPanorama(ctx, store.UpsertPanoramaParams{
					Provider: "yandex", Ref: YandexRef(p[0], p[1]), Lat: p[0], Lon: p[1],
					StreetID: &streetID, Source: "v0_streets",
				})
				if err != nil {
					return err
				}
				n++
			}
		}
		for _, m := range tk.Metro {
			if err := q.UpsertMetro(ctx, store.UpsertMetroParams{NameUz: m.Uz, NameRu: m.Ru, Lat: m.Lat, Lon: m.Lon}); err != nil {
				return err
			}
		}
		slog.Info("v0 pools imported", "points", len(locs.Points), "streets", len(tk.Streets), "street_points", n, "metro", len(tk.Metro))
		return Tag(ctx, q, false)
	})
}

// Tag records which places each panorama is in, and retires those outside the country.
func Tag(ctx context.Context, q *store.Queries, all bool) error {
	n, err := q.TagPanoramaPlaces(ctx, all)
	if err != nil {
		return err
	}
	out, err := q.RetireOutsidePanoramas(ctx)
	if err != nil {
		return err
	}
	slog.Info("panoramas tagged with places", "tagged", n, "outside", out)
	return nil
}

// CopyDemo gives the demo provider a twin of every Yandex panorama, so development and
// tests can play every map without spending Yandex views.
func CopyDemo(ctx context.Context, pool *pgxpool.Pool) error {
	n, err := store.New(pool).CopyPanoramasAsDemo(ctx)
	if err == nil {
		slog.Info("demo panoramas", "added", n)
	}
	return err
}

func readJSON(name string, v any) error {
	b, err := data.V0.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
