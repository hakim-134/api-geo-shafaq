// Package maps is everything playable: the official maps (the four v0 areas), later the
// players' own. A map is a set of places (and streets, and hand-picked panoramas) plus
// default rules and how rounds are picked.
package maps

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Pick says how a map's rounds are picked from its panoramas. A group (a street, a district
// or a region) is picked first, weighted by its size to the power Pow, then a panorama in
// it. So small towns still come up, and big ones do not take over.
type Pick struct {
	Group       string  `json:"group"`       // street | district | region | none
	Pow         float64 `json:"pow"`         // 0 = every group equal, 1 = by size
	SnapKm      float64 `json:"snapKm"`      // how far the opened panorama may be from the chosen one
	JitterKm    float64 `json:"jitterKm"`    // move the search point up to this far, for variety
	StreetsOnly bool    `json:"streetsOnly"` // only panoramas on named streets
}

type official struct {
	slug       string
	name, desc [3]string // uz, ru, en
	places     []string  // place slugs
	pick       Pick
	rules      rules.Rules
	unlisted   bool // off the menu; old games and rooms still open it by its slug
}

// The four v0 areas, as maps.
var officials = []official{
	{
		slug:   "tashkent-streets",
		name:   [3]string{"Toshkent ko‘chalari", "Улицы Ташкента", "Tashkent streets"},
		desc:   [3]string{"Asosiy ko‘chalar", "Главные улицы", "Main streets"}, // a sign carries a name, not a slogan
		places: []string{"toshkent"},
		pick:   Pick{Group: "street", Pow: 0.75, SnapKm: 0.12, StreetsOnly: true},
		rules:  rules.Easy,
	},
	{
		slug:   "tashkent",
		name:   [3]string{"Toshkent", "Ташкент", "Tashkent"},
		desc:   [3]string{"Poytaxt ko‘chalari, boshlash uchun", "Улицы столицы, для начала", "The capital's streets, a good start"},
		places: []string{"toshkent"},
		pick:   Pick{Group: "district", Pow: 0.5, SnapKm: 1, JitterKm: 0.1},
		rules:  rules.Easy,
		// one Tashkent on the home: "Toshkent ko‘chalari" (P1028 2.1)
		unlisted: true,
	},
	{
		slug: "historic",
		name: [3]string{"Tarixiy shaharlar", "Исторические города", "Historic cities"},
		desc: [3]string{"Samarqand, Buxoro, Xiva, Shahrisabz", "Самарканд, Бухара, Хива, Шахрисабз", "Samarkand, Bukhara, Khiva, Shahrisabz"},
		// each city with the district around it: the suburbs are part of the visit, the villages are not
		places: []string{"samarqand-shahri", "samarqand-tumani", "buxoro-shahri", "kogon-shahri", "kogon-tumani", "buxoro-tumani", "xiva-tumani", "shahrisabz-shahri"},
		pick:   Pick{Group: "district", Pow: 0.5, SnapKm: 1, JitterKm: 0.1},
		rules:  rules.Normal,
	},
	{
		slug:   "uzbekistan",
		name:   [3]string{"Butun O‘zbekiston", "Весь Узбекистан", "All Uzbekistan"},
		desc:   [3]string{"Toshkentdan Mo‘ynoqqacha", "От Ташкента до Муйнака", "From Tashkent to Moynaq"},
		places: []string{"uzbekistan"},
		pick:   Pick{Group: "region", Pow: 0.25, SnapKm: 1, JitterKm: 0.1},
		rules:  rules.Normal,
	},
}

// SeedOfficial creates or updates the official maps. Places must be imported first.
func SeedOfficial(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		for i, m := range officials {
			rulesJSON, _ := json.Marshal(m.rules)
			pickJSON, _ := json.Marshal(m.pick)
			visibility := "public"
			if m.unlisted {
				visibility = "unlisted"
			}
			id, err := q.UpsertOfficialMap(ctx, store.UpsertOfficialMapParams{
				Slug: m.slug, NameUz: m.name[0], NameRu: m.name[1], NameEn: m.name[2],
				DescUz: m.desc[0], DescRu: m.desc[1], DescEn: m.desc[2], Visibility: visibility,
				Rules: rulesJSON, Pick: pickJSON, Sort: int32(i),
			})
			if err != nil {
				return fmt.Errorf("map %s: %w", m.slug, err)
			}
			if err := q.ClearMapSources(ctx, id); err != nil {
				return err
			}
			for _, place := range m.places {
				if _, err := q.PlaceBySlug(ctx, place); err != nil {
					return fmt.Errorf("map %s: place %q: %w", m.slug, place, err)
				}
				if err := q.AddMapPlace(ctx, store.AddMapPlaceParams{MapID: id, PlaceSlug: place}); err != nil {
					return err
				}
			}
			slog.Info("official map", "slug", m.slug, "places", m.places)
		}
		return nil
	})
}
