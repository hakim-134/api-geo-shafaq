package maps

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Errors of the map maker, with the stable codes the API returns.
var (
	ErrBadMap  = errors.New("bad_input")
	ErrTooFew  = errors.New("too_few_panoramas")
	ErrTooMany = errors.New("too_many_maps")
)

const (
	MaxMaps      = 50 // a player's maps, at most
	MinPanoramas = 3  // fewer isn't a game
)

// Tags a public map can carry: a fixed few, so nobody has to moderate free words.
var Tags = []string{"mahalla", "shahar", "kocha", "tarixiy", "bozor", "metro", "oliygoh", "tabiat"}

// Sources is what a player's map is made of. Area is one drawn loop.
type Sources struct {
	Places  []string
	Streets []int64
	Area    []geo.Point
}

// Empty: nothing picked.
func (s Sources) Empty() bool { return len(s.Places) == 0 && len(s.Streets) == 0 && len(s.Area) == 0 }

// Valid checks the sizes and that a drawn area is a real loop.
func (s Sources) Valid() bool {
	if len(s.Places) > 30 || len(s.Streets) > 200 || len(s.Area) > 300 {
		return false
	}
	if len(s.Area) > 0 && len(s.Area) < 3 {
		return false
	}
	for _, p := range s.Area {
		if !p.Valid() {
			return false
		}
	}
	return true
}

// areaGeoJSON closes the loop; "" for no area.
func (s Sources) areaGeoJSON() string {
	if len(s.Area) == 0 {
		return ""
	}
	ring := make([][2]float64, 0, len(s.Area)+1)
	for _, p := range s.Area {
		ring = append(ring, [2]float64{p.Lon, p.Lat})
	}
	ring = append(ring, ring[0])
	b, _ := json.Marshal(map[string]any{"type": "Polygon", "coordinates": [][][2]float64{ring}})
	return string(b)
}

// Preview counts what the library would fill a map with, on the players' providers.
func (c *Catalog) Preview(ctx context.Context, s Sources) (store.PreviewSourcesRow, error) {
	return c.q.PreviewSources(ctx, store.PreviewSourcesParams{
		Providers: c.reg.Names(), Places: orNone(s.Places), Streets: orNoneI(s.Streets), Area: s.areaGeoJSON(),
	})
}

func orNone(x []string) []string {
	if x == nil {
		return []string{}
	}
	return x
}

func orNoneI(x []int64) []int64 {
	if x == nil {
		return []int64{}
	}
	return x
}

// playerPick: a map of streets alone plays like the street map, by street; anything else takes
// each panorama alike, and looks for it close by, so a round stays inside a drawn mahalla.
func playerPick(s Sources) Pick {
	if len(s.Places) == 0 && len(s.Area) == 0 {
		return Pick{Group: "street", Pow: 0.75, SnapKm: 0.12, StreetsOnly: true}
	}
	return Pick{Group: "none", SnapKm: 0.3}
}

// newSlug: a map's address, short enough for a chat; letters that don't read alike.
func newSlug() string {
	const letters = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 7)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return "m-" + string(b)
}

// ModerationFor: a public map waits for a person; any other needs nobody.
func ModerationFor(visibility string) string {
	if visibility == "public" {
		return "pending"
	}
	return "none"
}

// ValidTags keeps the tags to the fixed list.
func ValidTags(tags []string) bool {
	if len(tags) > 4 {
		return false
	}
	for _, t := range tags {
		if !slices.Contains(Tags, t) {
			return false
		}
	}
	return true
}

// CreatePlayerMap makes a player's map; the caller has checked the name.
func (c *Catalog) CreatePlayerMap(ctx context.Context, owner uuid.UUID, name, visibility string, tags []string, s Sources) (*Map, error) {
	if s.Empty() || !s.Valid() || !ValidTags(tags) {
		return nil, ErrBadMap
	}
	have, err := c.q.CountPlayerMaps(ctx, &owner)
	if err != nil {
		return nil, err
	}
	if int(have) >= MaxMaps {
		return nil, ErrTooMany
	}
	count, err := c.Preview(ctx, s)
	if err != nil {
		return nil, err
	}
	if int(count.Panoramas) < MinPanoramas {
		return nil, ErrTooFew
	}
	// a map of streets alone plays one round a street: one street isn't a game either
	if len(s.Places) == 0 && len(s.Area) == 0 && int(count.Streets) < MinPanoramas {
		return nil, ErrTooFew
	}
	rulesJSON, _ := json.Marshal(rules.Default)
	pickJSON, _ := json.Marshal(playerPick(s))
	if tags == nil {
		tags = []string{}
	}
	slug := newSlug()
	err = pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		id, err := q.CreatePlayerMap(ctx, store.CreatePlayerMapParams{
			Slug: slug, OwnerID: &owner, NameUz: name, Visibility: visibility,
			Moderation: ModerationFor(visibility), Tags: tags, Rules: rulesJSON, Pick: pickJSON,
		})
		if err != nil {
			return err
		}
		for _, p := range s.Places {
			if _, err := q.PlaceBySlug(ctx, p); errors.Is(err, pgx.ErrNoRows) {
				return ErrBadMap
			} else if err != nil {
				return err
			}
			if err := q.AddMapPlace(ctx, store.AddMapPlaceParams{MapID: id, PlaceSlug: p}); err != nil {
				return err
			}
		}
		for _, st := range s.Streets {
			if err := q.AddMapStreet(ctx, store.AddMapStreetParams{MapID: id, StreetID: &st}); err != nil {
				return fmt.Errorf("street %d: %w", st, ErrBadMap)
			}
		}
		if a := s.areaGeoJSON(); a != "" {
			return q.AddMapArea(ctx, store.AddMapAreaParams{MapID: id, Area: a})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, slug)
}

// ---------- streets I miss ----------

// MissPrefix starts the slug of a player's map of missed streets, one for each player.
const MissPrefix = "miss-"

// MissMap is the slug of this player's map of missed streets.
func MissMap(user uuid.UUID) string { return MissPrefix + user.String() }

// IsMiss says whether a slug is any player's map of missed streets.
func IsMiss(slug string) bool { return strings.HasPrefix(slug, MissPrefix) }

// SyncMissed makes the player's map of missed streets what it is right now: a street guessed
// within LearnedKm leaves it, one missed since joins it. Every street counts alike, scored and
// framed as the whole street map, so the box doesn't tell where the last street is.
func (c *Catalog) SyncMissed(ctx context.Context, user uuid.UUID) error {
	slug := MissMap(user)
	rulesJSON, _ := json.Marshal(rules.Default)
	pickJSON, _ := json.Marshal(Pick{Group: "street", Pow: 0, SnapKm: 0.12, StreetsOnly: true, Frame: "tashkent-streets"})
	err := pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		id, err := q.UpsertMissMap(ctx, store.UpsertMissMapParams{
			Slug: slug, OwnerID: &user, NameUz: "Adashgan ko‘chalarim", NameRu: "Улицы, где я ошибся", NameEn: "Streets I miss",
			Rules: rulesJSON, Pick: pickJSON,
		})
		if err != nil {
			return err
		}
		if err := q.ClearMapSources(ctx, id); err != nil {
			return err
		}
		return q.AddMissedStreets(ctx, store.AddMissedStreetsParams{MapID: id, UserID: user, LearnedKm: places.LearnedKm})
	})
	c.Forget(slug)
	return err
}
