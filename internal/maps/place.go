package maps

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// PlacePrefix starts the slug of a place's own map: "place-chilonzor-tumani". Every place
// can be played; its map is made the first time someone picks it.
const PlacePrefix = "place-"

// PlaceMap is the slug of a place's own map.
func PlaceMap(placeSlug string) string { return PlacePrefix + placeSlug }

// PlacePick is how a place's map picks rounds, by what its progress counts: Tashkent and
// its districts by street (only street panoramas, as the street map), a region by district,
// the country by region, a small town by spot.
func PlacePick(p *places.Place) Pick {
	switch p.Unit() {
	case places.UnitStreet:
		return Pick{Group: "street", Pow: 0.75, SnapKm: 0.12, StreetsOnly: true}
	case places.UnitRegion:
		return Pick{Group: "region", Pow: 0.25, SnapKm: 1, JitterKm: 0.1}
	case places.UnitDistrict:
		return Pick{Group: "district", Pow: 0.5, SnapKm: 1, JitterKm: 0.1}
	}
	return Pick{Group: "none", SnapKm: 1, JitterKm: 0.1}
}

func (c *Catalog) makePlaceMap(ctx context.Context, slug string) error {
	c.making.Lock()
	defer c.making.Unlock()
	if _, err := c.q.GetMapBySlug(ctx, PlaceMap(slug)); err == nil {
		return nil // made meanwhile
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	t, err := c.tree.Get(ctx)
	if err != nil {
		return err
	}
	p := t.Get(slug)
	if p == nil {
		return ErrNotFound
	}
	var top places.Text // the sign's small line: where the place is
	if p.Parent != nil {
		top = p.Parent.Name
	}
	rulesJSON, _ := json.Marshal(rules.Normal)
	pickJSON, _ := json.Marshal(PlacePick(p))
	id, err := c.q.UpsertPlaceMap(ctx, store.UpsertPlaceMapParams{
		Slug: PlaceMap(slug), NameUz: p.Name.Uz, NameRu: p.Name.Ru, NameEn: p.Name.En,
		DescUz: top.Uz, DescRu: top.Ru, DescEn: top.En, Rules: rulesJSON, Pick: pickJSON,
	})
	if err != nil {
		return err
	}
	if err := c.q.ClearMapSources(ctx, id); err != nil {
		return err
	}
	return c.q.AddMapPlace(ctx, store.AddMapPlaceParams{MapID: id, PlaceSlug: slug})
}
