package game

import (
	"context"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// twoStreets puts the west district's panoramas on one street and the east's on another, and adds
// the street map a missed map takes its frame from.
func twoStreets(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO streets (place_id, name_uz) SELECT id, 'G‘arb ko‘chasi' FROM places WHERE slug = 'west'`,
		`INSERT INTO streets (place_id, name_uz) SELECT id, 'Sharq ko‘chasi' FROM places WHERE slug = 'east'`,
		`UPDATE panoramas p SET street_id = s.id FROM streets s JOIN places pl ON pl.id = s.place_id WHERE ST_Contains(pl.geom, p.pos)`,
		`INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('tashkent-streets', 'Ko‘chalar', '{"rounds":5}', '{"group":"street","pow":0.75,"snapKm":0.12,"streetsOnly":true}')`,
		`INSERT INTO map_sources (map_id, place_id) SELECT m.id, p.id FROM maps m, places p WHERE m.slug = 'tashkent-streets' AND p.slug = 'toshkent'`,
	} {
		if _, err := f.pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

// A street guessed far off is on the player's map of missed streets; guessed within 500 m it leaves.
func TestStreetsIMiss(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	twoStreets(t, f)
	q := store.New(f.pool)
	slug := maps.MissMap(f.user)
	demo := []string{"demo"}
	missed := func() []store.MissedStreetsRow {
		t.Helper()
		rows, err := q.MissedStreets(ctx, store.MissedStreetsParams{UserID: f.user, Providers: demo, LearnedKm: places.LearnedKm})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	// a round on the street map, guessed in another country
	g, err := f.svc.Create(ctx, f.user, "tashkent-streets", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := f.svc.NextRound(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Shown(ctx, f.user, g.ID, rd.N, nil); err != nil {
		t.Fatal(err)
	}
	far := geo.Point{Lat: 40, Lon: 60}
	if _, err := f.svc.Guess(ctx, f.user, g.ID, rd.N, &far); err != nil {
		t.Fatal(err)
	}
	if n := len(missed()); n != 1 {
		t.Fatalf("%d streets missed, want the one guessed far off", n)
	}

	if err := f.svc.Catalog.SyncMissed(ctx, f.user); err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.Catalog.Get(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Open(f.user) || m.Open(f.other) {
		t.Fatal("the map of missed streets is the player's own")
	}
	frame, _ := f.svc.Catalog.Get(ctx, "tashkent-streets")
	if m.BBox != frame.BBox || m.SizeKm != frame.SizeKm {
		t.Fatalf("framed %v, want the street map's %v: the box would give the street away", m.BBox, frame.BBox)
	}

	// one street on the map: one round, however many panoramas the street has
	miss, err := f.svc.Create(ctx, f.user, slug, testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if miss.Rules.Rounds != 1 {
		t.Fatalf("%d rounds on one missed street", miss.Rules.Rounds)
	}
	rd, err = f.svc.NextRound(ctx, f.user, miss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Shown(ctx, f.user, miss.ID, rd.N, nil); err != nil {
		t.Fatal(err)
	}
	var at geo.Point
	if err := f.pool.QueryRow(ctx, `SELECT ST_Y(p.pos), ST_X(p.pos) FROM rounds r JOIN panoramas p ON p.id = r.panorama_id
		WHERE r.game_id = $1 AND r.n = 1`, miss.ID).Scan(&at.Lat, &at.Lon); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Guess(ctx, f.user, miss.ID, rd.N, &at); err != nil {
		t.Fatal(err)
	}
	if n := len(missed()); n != 0 {
		t.Fatalf("%d streets still missed after the guess on the spot", n)
	}
	if err := f.svc.Catalog.SyncMissed(ctx, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, f.user, slug, testRules("demo")); err != ErrNoPanorama {
		t.Fatalf("a game on an empty list: %v", err)
	}
}
