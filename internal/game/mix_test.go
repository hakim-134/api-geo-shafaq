package game

import (
	"context"
	"math"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/score"
)

// A mix of the small west district and a big region far away: a 1 km miss in the district
// scores as on the district's own map, not as on the mix's box of 500 km and more.
func TestMixedGameScoresEachRoundByItsOwnPart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO places (kind, slug, name_uz, geom, geom_simple, size_km, parent_id)
		SELECT 'region', 'buxoro', 'Buxoro', e, e, 340, (SELECT id FROM places WHERE slug = 'uzbekistan')
		FROM (SELECT ST_Multi(ST_MakeEnvelope(63, 39, 66, 41, 4326)) AS e) x`)
	for i := range 6 {
		exec(`INSERT INTO panoramas (provider, ref, pos, source) VALUES ('demo', $1, ST_SetSRID(ST_MakePoint($2, 40), 4326), 'test')`,
			"bux-"+string(rune('a'+i)), 63.5+float64(i)*0.4)
	}
	exec(`UPDATE panoramas p SET place_ids = (SELECT array_agg(pl.id ORDER BY pl.size_km DESC) FROM places pl WHERE ST_Contains(pl.geom, p.pos))`)

	m, err := f.svc.Catalog.Mix(ctx, []string{"place-west", "place-buxoro"})
	if err != nil {
		t.Fatal(err)
	}
	west, bux := m.Parts()[0], m.Parts()[1]
	if west.SizeKm > 30 || bux.SizeKm < 200 || m.SizeKm < 500 {
		t.Fatalf("sizes: west %.0f, buxoro %.0f, the mix %.0f", west.SizeKm, bux.SizeKm, m.SizeKm)
	}
	rs := testRules("demo")
	rs.Rounds = 4
	rs.Hints = rules.Hints{Count: 4, Per: rules.PerGame, Kind: rules.HintCircle, Cost: 0.5}
	g, err := f.svc.Create(ctx, f.user, m.Slug, rs)
	if err != nil {
		t.Fatal(err)
	}

	turns := map[*maps.Map]int{}
	var scores []int
	var bases []int
	for n := 1; n <= 4; n++ {
		if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Shown(ctx, f.user, g.ID, n, nil); err != nil {
			t.Fatal(err)
		}
		var actual geo.Point
		var sizeKm float64
		err := f.pool.QueryRow(ctx, `SELECT ST_Y(r.actual), ST_X(r.actual), r.size_km FROM rounds r WHERE r.game_id = $1 AND r.n = $2`, g.ID, n).
			Scan(&actual.Lat, &actual.Lon, &sizeKm)
		if err != nil {
			t.Fatal(err)
		}
		part := west
		if actual.Lon < 66.5 {
			part = bux
		}
		turns[part]++
		if sizeKm != part.SizeKm {
			t.Fatalf("round %d is kept with its part's size %.1f, not %.1f", n, part.SizeKm, sizeKm)
		}
		hints := 0
		if n%2 == 1 { // a hint is the part's size too: a circle of 4% of it, at least 1 km
			h, err := f.svc.Hint(ctx, f.user, g.ID, n)
			if err != nil {
				t.Fatal(err)
			}
			if want := math.Max(part.SizeKm*0.04, 1); math.Abs(h.RadiusKm-want) > 1e-9 {
				t.Fatalf("round %d's hint: a circle of %.2f km, want %.2f", n, h.RadiusKm, want)
			}
			hints = 1
		}
		guess := geo.Offset(actual, 1, 0)
		res, err := f.svc.Guess(ctx, f.user, g.ID, n, &guess)
		if err != nil {
			t.Fatal(err)
		}
		before := score.Round(*res.Km, part.SizeKm, rs.ScoreScale)
		if want := score.WithHints(before, hints, rs.Hints.Cost); res.Score != want {
			t.Fatalf("round %d (%s): a 1 km miss scores %d, want %d as on its own map", n, part.Slug, res.Score, want)
		}
		if res.Score == score.WithHints(score.Round(*res.Km, m.SizeKm, rs.ScoreScale), hints, rs.Hints.Cost) {
			t.Fatalf("round %d: the part's size and the mix's score alike; the test shows nothing", n)
		}
		scores = append(scores, res.Score)
		if hints == 0 {
			before = 0
		}
		bases = append(bases, before)
	}
	if turns[west] != 2 || turns[bux] != 2 {
		t.Fatalf("two parts, four rounds: two each, got west %d, buxoro %d", turns[west], turns[bux])
	}

	// read back later (history, the result page): the same scores, the base before hints too
	got, err := f.svc.Get(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rounds) != len(scores) {
		t.Fatalf("read back %d rounds, want %d", len(got.Rounds), len(scores))
	}
	for i, rd := range got.Rounds {
		if rd.Result == nil || rd.Result.Score != scores[i] {
			t.Fatalf("round %d read back: %+v, want %d", i+1, rd.Result, scores[i])
		}
		if rd.Result.Base != bases[i] {
			t.Fatalf("round %d base score: %d, want %d", i+1, rd.Result.Base, bases[i])
		}
	}

	// a challenge of it plays each round by the same size
	code, err := f.svc.CreateChallenge(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	cg, err := f.svc.PlayChallenge(ctx, f.other, code)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		if _, err := f.svc.NextRound(ctx, f.other, cg.ID); err != nil {
			t.Fatal(err)
		}
		var mine, theirs float64
		if err := f.pool.QueryRow(ctx, `SELECT a.size_km, b.size_km FROM rounds a, rounds b
			WHERE a.game_id = $1 AND b.game_id = $2 AND a.n = $3 AND b.n = $3`, g.ID, cg.ID, n).Scan(&mine, &theirs); err != nil {
			t.Fatal(err)
		}
		if mine != theirs {
			t.Fatalf("challenge round %d: size %.1f, the source's %.1f", n, theirs, mine)
		}
		if _, err := f.svc.Shown(ctx, f.other, cg.ID, n, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Guess(ctx, f.other, cg.ID, n, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// Rounds from before mixed maps have no size of their own: they are read with the game's.
func TestOldRoundsScoreByTheGamesSize(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	rs := testRules("demo")
	g, err := f.svc.Create(ctx, f.user, "city", rs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	// as a round written before the migration, in a game whose map has grown since
	if _, err := f.pool.Exec(ctx, `UPDATE rounds SET size_km = NULL WHERE game_id = $1`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE games SET size_km = 100 WHERE id = $1`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Shown(ctx, f.user, g.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Hint(ctx, f.user, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	var actual geo.Point
	if err := f.pool.QueryRow(ctx, `SELECT ST_Y(actual), ST_X(actual) FROM rounds WHERE game_id = $1 AND n = 1`, g.ID).
		Scan(&actual.Lat, &actual.Lon); err != nil {
		t.Fatal(err)
	}
	guess := geo.Offset(actual, 1, 0)
	res, err := f.svc.Guess(ctx, f.user, g.ID, 1, &guess)
	if err != nil {
		t.Fatal(err)
	}
	before := score.Round(*res.Km, 100, rs.ScoreScale)
	if res.Score != score.WithHints(before, 1, rs.Hints.Cost) {
		t.Fatalf("an old round is scored by the game's size: %d", res.Score)
	}
	got, err := f.svc.Get(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Rounds[0].Result; r == nil || r.Score != res.Score || r.Base != before {
		t.Fatalf("read back: %+v, base %d", r, before)
	}
}
