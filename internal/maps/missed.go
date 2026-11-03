package maps

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

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
