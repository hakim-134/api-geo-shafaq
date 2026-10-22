package places

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Text is a name in the game's three languages.
type Text struct {
	Uz string `json:"uz"`
	Ru string `json:"ru"`
	En string `json:"en"`
}

// NewText fills empty Russian and English names with the Uzbek one, as street signs do.
func NewText(uz, ru, en string) Text {
	if ru == "" {
		ru = uz
	}
	if en == "" {
		en = uz
	}
	return Text{Uz: uz, Ru: ru, En: en}
}

// Info is "you were here": what a round teaches about its place.
type Info struct {
	Street   *Text
	District *Text
	City     *Text
	Region   *Text
	Metro    *Metro
}

type Metro struct {
	Name Text
	Km   float64
}

// The street a place is on must be really close; the nearest metro only within walking-ish
// distance, or it teaches nothing.
const (
	streetMaxM = 150
	metroMaxKm = 3
)

// Lookup answers where a point is.
func Lookup(ctx context.Context, q *store.Queries, p geo.Point) (Info, error) {
	var info Info
	at, err := q.PlacesAt(ctx, store.PlacesAtParams{Lat: p.Lat, Lon: p.Lon})
	if err != nil {
		return info, err
	}
	for _, pl := range at { // smallest first
		t := NewText(pl.NameUz, pl.NameRu, pl.NameEn)
		switch pl.Kind {
		case "district":
			if info.District == nil {
				info.District = &t
			}
		case "city":
			if info.City == nil {
				info.City = &t
			}
		case "region":
			if info.Region == nil {
				info.Region = &t
			}
		}
	}

	st, err := q.NearestStreet(ctx, store.NearestStreetParams{Lat: p.Lat, Lon: p.Lon, MaxM: streetMaxM})
	switch {
	case err == nil:
		t := NewText(st.NameUz, st.NameRu, "")
		info.Street = &t
	case !errors.Is(err, pgx.ErrNoRows):
		return info, err
	}

	m, err := q.NearestMetro(ctx, store.NearestMetroParams{Lat: p.Lat, Lon: p.Lon})
	switch {
	case err == nil && m.Km <= metroMaxKm:
		info.Metro = &Metro{Name: NewText(m.NameUz, m.NameRu, ""), Km: m.Km}
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return info, err
	}
	return info, nil
}
