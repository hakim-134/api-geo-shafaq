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

func TestPlayerMaps(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	twoStreets(t, f)
	c := f.svc.Catalog
	// a small box in the fixture's grid, drawn by a finger that crossed itself on the way back
	box := []geo.Point{{Lat: 41.205, Lon: 69.105}, {Lat: 41.205, Lon: 69.125}, {Lat: 41.275, Lon: 69.125}, {Lat: 41.275, Lon: 69.105}}
	bow := []geo.Point{box[0], box[1], box[3], box[2]}

	n, err := c.Preview(ctx, maps.Sources{Area: box})
	if err != nil {
		t.Fatal(err)
	}
	if n.Panoramas != 4 {
		t.Fatalf("%d panoramas in the drawn box, want 4", n.Panoramas)
	}
	if _, err := c.Preview(ctx, maps.Sources{Area: bow}); err != nil {
		t.Fatalf("a loop that crosses itself: %v", err)
	}
	if _, err := c.CreatePlayerMap(ctx, f.user, "Hech narsa", "unlisted", nil, maps.Sources{Area: []geo.Point{{Lat: 10, Lon: 10}, {Lat: 10, Lon: 11}, {Lat: 11, Lon: 11}}}); err != maps.ErrTooFew {
		t.Fatalf("a map over no panoramas: %v", err)
	}
	if _, err := c.CreatePlayerMap(ctx, f.user, "Teglar", "public", []string{"bad-tag"}, maps.Sources{Area: box}); err != maps.ErrBadMap {
		t.Fatalf("a tag off the list: %v", err)
	}

	m, err := c.CreatePlayerMap(ctx, f.user, "Mening mahallam", "private", nil, maps.Sources{Area: box})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Count(f.svc.Reg.Allowed(nil)); got != 4 {
		t.Fatalf("the map holds %d, the preview said 4", got)
	}
	if m.SizeKm < 5 {
		t.Fatalf("a small map plays on %v km: too strict to score", m.SizeKm)
	}
	if !m.Open(f.user) || m.Open(f.other) {
		t.Fatal("a private map is its owner's")
	}
	g, err := f.svc.Create(ctx, f.user, m.Slug, testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}

	// places and streets fill it too; a map of streets alone plays by street
	both, err := c.CreatePlayerMap(ctx, f.user, "Ikkalasi", "unlisted", nil, maps.Sources{Places: []string{"west"}, Area: box})
	if err != nil {
		t.Fatal(err)
	}
	if !both.Open(f.other) {
		t.Fatal("an unlisted map opens by its link")
	}
	var streets []int64
	rows, err := f.pool.Query(ctx, `SELECT id FROM streets ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		streets = append(streets, id)
	}
	if _, err := c.CreatePlayerMap(ctx, f.user, "Bitta ko‘cha", "unlisted", nil, maps.Sources{Streets: streets[:1]}); err != maps.ErrTooFew {
		t.Fatalf("a map of one street: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO streets (place_id, name_uz) SELECT id, 'Uchinchi ko‘cha' FROM places WHERE slug = 'west'`); err != nil {
		t.Fatal(err)
	}
	var third int64
	if err := f.pool.QueryRow(ctx, `UPDATE panoramas SET street_id = (SELECT id FROM streets WHERE name_uz = 'Uchinchi ko‘cha')
		WHERE id IN (SELECT id FROM panoramas WHERE street_id = $1 AND provider = 'yandex' LIMIT 2) RETURNING street_id`, streets[0]).Scan(&third); err != nil {
		t.Fatal(err)
	}
	st, err := c.CreatePlayerMap(ctx, f.user, "Uch ko‘cha", "unlisted", nil, maps.Sources{Streets: append(streets, third)})
	if err != nil {
		t.Fatal(err)
	}
	if st.Pick.Group != "street" || !st.Pick.StreetsOnly {
		t.Fatalf("a map of streets picks %+v", st.Pick)
	}
	if m.Outline == "" {
		t.Fatal("a drawn map's sign has no loop")
	}

	// the public queue: pending isn't listed, approved is
	pub, err := c.CreatePlayerMap(ctx, f.user, "Hammaga", "public", []string{"mahalla"}, maps.Sources{Area: box})
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(f.pool)
	search := func(s string) int {
		t.Helper()
		rows, err := q.SearchMaps(ctx, store.SearchMapsParams{Q: s, Lim: 20})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if search("Hammaga") != 0 {
		t.Fatal("a public map is listed before a person approved it")
	}
	if _, err := q.ModerateMap(ctx, store.ModerateMapParams{Slug: pub.Slug, Moderation: "approved"}); err != nil {
		t.Fatal(err)
	}
	if search("hammaga") != 1 {
		t.Fatal("an approved map isn't found by its name")
	}
	if search("Mening") != 0 || search("Ikkalasi") != 0 {
		t.Fatal("a private or unlisted map came up in the search")
	}
	if err := q.LikeMap(ctx, store.LikeMapParams{MapID: pub.ID, UserID: f.other}); err != nil {
		t.Fatal(err)
	}
	stats, err := q.MapStats(ctx, store.MapStatsParams{MapID: pub.ID, UserID: f.other})
	if err != nil || stats.Likes != 1 || !stats.Liked {
		t.Fatalf("stats %+v, %v", stats, err)
	}
}
