package game

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
	"github.com/hakim-134/api-geo-shafaq/internal/testdb"
)

// fixture: a square "country", a 0.2° "city" inside it with two districts, 30 panoramas
// for each of yandex and demo, and a map of the city.
type fixture struct {
	svc   *Service
	pool  *pgxpool.Pool
	clock time.Time
	user  uuid.UUID
	other uuid.UUID
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	square := func(w, s, e, n float64) string {
		return fmt.Sprintf("ST_Multi(ST_MakeEnvelope(%f, %f, %f, %f, 4326))", w, s, e, n)
	}
	place := func(kind, slug, name string, sizeKm float64, env string) {
		exec(fmt.Sprintf(`INSERT INTO places (kind, slug, name_uz, geom, geom_simple, size_km) VALUES ($1, $2, $3, %s, %s, $4)`, env, env),
			kind, slug, name, sizeKm)
	}
	place("country", "uzbekistan", "O‘zbekiston", 1700, square(56, 37, 73, 46))
	place("city", "toshkent", "Toshkent", 40, square(69.1, 41.2, 69.3, 41.4))
	place("district", "west", "G‘arb tumani", 20, square(69.1, 41.2, 69.2, 41.4))
	place("district", "east", "Sharq tumani", 20, square(69.2, 41.2, 69.3, 41.4))
	exec(`UPDATE places p SET parent_id = (SELECT id FROM places WHERE slug = 'uzbekistan') WHERE slug = 'toshkent'`)
	exec(`UPDATE places p SET parent_id = (SELECT id FROM places WHERE slug = 'toshkent') WHERE kind = 'district'`)
	for i := range 30 {
		lat, lon := 41.21+float64(i%10)*0.018, 69.11+float64(i/10)*0.06
		for _, prov := range []string{"yandex", "demo"} {
			exec(`INSERT INTO panoramas (provider, ref, pos, source) VALUES ($1, $2, ST_SetSRID(ST_MakePoint($4, $3), 4326), 'test')`,
				prov, fmt.Sprintf("%s-%d", prov, i), lat, lon)
		}
	}
	exec(`UPDATE panoramas p SET place_ids = (SELECT array_agg(pl.id ORDER BY pl.size_km DESC) FROM places pl WHERE ST_Contains(pl.geom, p.pos))`)
	exec(`INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('city', 'Shahar', $1, '{"group":"district","pow":0.5,"snapKm":0.12}')`, `{"rounds":5}`)
	exec(`INSERT INTO map_sources (map_id, place_id) SELECT m.id, p.id FROM maps m, places p WHERE m.slug = 'city' AND p.slug = 'toshkent'`)

	q := store.New(pool)
	reg, err := provider.NewRegistry([]string{"yandex"}, []string{"demo"}, provider.Yandex{}, provider.Demo{Secret: []byte("t")})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, clock: time.Now()}
	f.svc = &Service{Pool: pool, Catalog: maps.NewCatalog(q, reg, &places.Cache{Q: q, Providers: reg.Names()}), Reg: reg, Usage: provider.Usage{Q: q}, Now: func() time.Time { return f.clock }}
	for _, id := range []*uuid.UUID{&f.user, &f.other} {
		u, err := q.CreateUser(ctx, store.CreateUserParams{Name: "🐼_01", Avatar: "🐼"})
		if err != nil {
			t.Fatal(err)
		}
		*id = u.ID
	}
	return f
}

func testRules(providers ...string) *rules.Rules {
	r := rules.Default
	r.Rounds = 2
	r.TimeSec = 30
	r.Hints = rules.Hints{Count: 1, Per: rules.PerGame, Cost: 0.5}
	r.Providers = providers
	return &r
}

func TestYandexRoundIsScoredAgainstWhatTheViewerOpened(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, err := f.svc.Create(ctx, f.user, "city", testRules("yandex"))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := f.svc.NextRound(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rd.View.Near == nil || rd.View.SnapKm != 0.12 {
		t.Fatalf("a yandex round needs a search point and the snap: %+v", rd.View)
	}
	if rd.View.GuessMap != "yandex" {
		t.Fatalf("a yandex round is guessed on Yandex's map (its terms): %+v", rd.View)
	}
	near := *rd.View.Near

	if _, err := f.svc.Shown(ctx, f.user, g.ID, 1, &geo.Point{Lat: near.Lat + 0.01, Lon: near.Lon}); !errors.Is(err, ErrTooFar) {
		t.Fatalf("a panorama 1 km away was accepted: %v", err)
	}
	opened := geo.Offset(near, 0.05, 1) // 50 m away: fine
	if _, err := f.svc.Shown(ctx, f.user, g.ID, 1, &opened); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Guess(ctx, f.user, g.ID, 1, &opened)
	if err != nil {
		t.Fatal(err)
	}
	if res.Score != 5000 || res.Actual != opened {
		t.Fatalf("a guess on the opened panorama should be perfect: %+v", res)
	}
	if res.Place.District == nil || res.Place.City == nil {
		t.Fatalf("no place info: %+v", res.Place)
	}
}

func TestClock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, _ := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	_, _ = f.svc.NextRound(ctx, f.user, g.ID)

	// showing late does not buy time: the clock starts at most a minute after the round
	f.clock = f.clock.Add(5 * time.Minute)
	rd, err := f.svc.Shown(ctx, f.user, g.ID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Remaining == nil || *rd.Remaining != 0 {
		t.Fatalf("a round shown after 5 minutes should have no time left: %+v", rd.Remaining)
	}
	pos := geo.Point{Lat: 41.3, Lon: 69.2}
	res, err := f.svc.Guess(ctx, f.user, g.ID, 1, &pos)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Late || res.Score != 0 {
		t.Fatalf("a late guess scored: %+v", res)
	}

	// within the grace period it counts
	_, _ = f.svc.NextRound(ctx, f.user, g.ID)
	_, _ = f.svc.Shown(ctx, f.user, g.ID, 2, nil)
	f.clock = f.clock.Add(30*time.Second + 2*time.Second)
	res, err = f.svc.Guess(ctx, f.user, g.ID, 2, &pos)
	if err != nil {
		t.Fatal(err)
	}
	if res.Late || res.Score == 0 || !res.GameOver {
		t.Fatalf("a guess 2 s after the deadline should count and end the game: %+v", res)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); !errors.Is(err, ErrGameOver) {
		t.Fatalf("a third round of two: %v", err)
	}
}

func TestHintsAndRepeats(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, _ := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	_, _ = f.svc.NextRound(ctx, f.user, g.ID)
	_, _ = f.svc.Shown(ctx, f.user, g.ID, 1, nil)
	h, err := f.svc.Hint(ctx, f.user, g.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.Kind != "district" || h.RadiusKm < 1 {
		t.Fatalf("hint on a city map: %+v", h)
	}
	if _, err := f.svc.Hint(ctx, f.user, g.ID, 1); !errors.Is(err, ErrNoHints) {
		t.Fatalf("a second hint of one per game: %v", err)
	}
	game, _ := f.svc.Get(ctx, f.user, g.ID)
	actual := game.Rounds[0].View // demo: no position in the view
	if actual.Near != nil {
		t.Fatal("the demo view leaked a position")
	}
	rows, _ := store.New(f.pool).GameRounds(ctx, store.GameRoundsParams{GameID: g.ID, UserID: f.user})
	exact := geo.Point{Lat: rows[0].ActualLat, Lon: rows[0].ActualLon}
	first, err := f.svc.Guess(ctx, f.user, g.ID, 1, &exact)
	if err != nil {
		t.Fatal(err)
	}
	if first.Score != 2500 || first.Base != 5000 {
		t.Fatalf("a perfect guess with one hint at 50%% should be 2500 of 5000, got %d of %d", first.Score, first.Base)
	}
	if read, _ := f.svc.Get(ctx, f.user, g.ID); read.Rounds[0].Result.Base != 5000 {
		t.Fatalf("the game page should show the score before the hint too: %+v", read.Rounds[0].Result)
	}
	again, err := f.svc.Guess(ctx, f.user, g.ID, 1, &geo.Point{Lat: 41.2, Lon: 69.1})
	if err != nil || again.Score != first.Score {
		t.Fatalf("a second guess changed the round: %+v, %v", again, err)
	}
	// round 2 must not repeat round 1's panorama, and per-game hints are used up
	rd, _ := f.svc.NextRound(ctx, f.user, g.ID)
	if rd.View.Ref == game.Rounds[0].View.Ref {
		t.Fatal("the same panorama twice in a game")
	}
	if rd.HintsLeft != 0 {
		t.Fatalf("hints left in round 2: %d", rd.HintsLeft)
	}
}

func TestFailedPanoramaIsReplacedAndRetired(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, _ := f.svc.Create(ctx, f.user, "city", testRules("yandex"))
	first, _ := f.svc.NextRound(ctx, f.user, g.ID)
	second, err := f.svc.Failed(ctx, f.user, g.ID, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if *second.View.Near == *first.View.Near {
		t.Fatal("the failed panorama came back")
	}
	for range 4 {
		if _, err := f.svc.Failed(ctx, f.user, g.ID, 1, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Failed(ctx, f.user, g.ID, 1, true); !errors.Is(err, ErrTooManyFails) {
		t.Fatalf("a sixth swap: %v", err)
	}
}

func TestOthersCannotTouchARunningGame(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, _ := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	_, _ = f.svc.NextRound(ctx, f.user, g.ID)
	if _, err := f.svc.Get(ctx, f.other, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another player read a running game: %v", err)
	}
	if _, err := f.svc.Guess(ctx, f.other, g.ID, 1, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another player guessed: %v", err)
	}
}

func TestPlayersNeverGetTestPanoramas(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := testRules() // no providers named: the players' ones (yandex), never demo
	r.Rounds = 10
	g, err := f.svc.Create(ctx, f.user, "city", r)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= r.Rounds; n++ {
		rd, err := f.svc.NextRound(ctx, f.user, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rd.View.Provider != "yandex" {
			t.Fatalf("round %d is on %q", n, rd.View.Provider)
		}
		if _, err := f.svc.Guess(ctx, f.user, g.ID, n, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBadRules(t *testing.T) {
	f := setup(t)
	r := testRules("google")
	if _, err := f.svc.Create(context.Background(), f.user, "city", r); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown provider accepted: %v", err)
	}
	r = testRules()
	r.Rounds = 20
	r.Providers = []string{"demo"}
	if _, err := f.svc.Create(context.Background(), f.user, "nowhere", r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown map: %v", err)
	}
}

func TestDailyCap(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	q := store.New(f.pool)
	f.svc.Usage.Caps = map[string]int{"yandex": 3}
	view := func() {
		t.Helper()
		if _, err := q.CountProviderView(ctx, store.CountProviderViewParams{Day: provider.Today(time.Now()), Provider: "yandex"}); err != nil {
			t.Fatal(err)
		}
	}
	play := func(g *Game) []string {
		t.Helper()
		var got []string
		for n := 1; n <= g.Rules.Rounds; n++ {
			rd, err := f.svc.NextRound(ctx, f.user, g.ID)
			if err != nil {
				t.Fatalf("round %d: %v", n, err)
			}
			got = append(got, rd.View.Provider)
			if _, err := f.svc.Guess(ctx, f.user, g.ID, n, nil); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}

	// 2 of 3 used: a yandex game starts, reaches the cap in round 1 and still finishes on it
	view()
	view()
	g, err := f.svc.Create(ctx, f.user, "city", testRules("yandex"))
	if err != nil {
		t.Fatal(err)
	}
	if got := play(g); got[0] != "yandex" || got[1] != "yandex" {
		t.Fatalf("a started game must finish on its provider: %v", got)
	}

	// at the cap: a game with another provider uses it, a yandex-only game cannot start
	g, err = f.svc.Create(ctx, f.user, "city", testRules("yandex", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got := play(g); got[0] != "demo" || got[1] != "demo" {
		t.Fatalf("rounds past the cap should fall back to demo: %v", got)
	}
	if _, err := f.svc.Create(ctx, f.user, "city", testRules("yandex")); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("a yandex game started past the cap: %v", err)
	}

	// no cap: plays as before
	f.svc.Usage.Caps = map[string]int{"yandex": 0}
	if _, err := f.svc.Create(ctx, f.user, "city", testRules("yandex")); err != nil {
		t.Fatalf("cap 0 means no cap: %v", err)
	}
}

// playRound shows and guesses round n on the demo provider (no position needed).
func playRound(t *testing.T, f *fixture, id uuid.UUID, n int) *Result {
	t.Helper()
	ctx := context.Background()
	if _, err := f.svc.NextRound(ctx, f.user, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Shown(ctx, f.user, id, n, nil); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Guess(ctx, f.user, id, n, &geo.Point{Lat: 41.3, Lon: 69.2})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEndlessGameEndsWhenThePlayerSays(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := testRules("demo")
	r.Rounds = 0
	g, err := f.svc.Create(ctx, f.user, "city", r)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		if res := playRound(t, f, g.ID, n); res.GameOver {
			t.Fatalf("round %d ended an endless game", n)
		}
	}
	// not while a round is on screen
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Shown(ctx, f.user, g.ID, 4, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Finish(ctx, f.user, g.ID); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("finished in the middle of a round: %v", err)
	}
	if _, err := f.svc.Guess(ctx, f.user, g.ID, 4, nil); err != nil {
		t.Fatal(err)
	}
	done, err := f.svc.Finish(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "finished" || done.Max() != 4*5000 {
		t.Fatalf("status %s, max %d", done.Status, done.Max())
	}
	items, _ := f.svc.History(ctx, f.user, nil, 0, 5)
	if len(items) != 1 || items[0].Rounds != 4 {
		t.Fatalf("history: %+v", items)
	}
	// numbered pages: the second page of one game is empty, and the count says one
	if items, _ := f.svc.History(ctx, f.user, nil, 5, 5); len(items) != 0 {
		t.Fatalf("page 2: %+v", items)
	}
	if n, err := store.New(f.pool).CountUserGames(ctx, f.user); err != nil || n != 1 {
		t.Fatalf("count %d, %v", n, err)
	}

	// a game finished before its first guess is dropped, not kept as a 0
	g2, _ := f.svc.Create(ctx, f.user, "city", r)
	if out, err := f.svc.Finish(ctx, f.user, g2.ID); err != nil || out.Status != "abandoned" {
		t.Fatalf("empty game: %v %+v", err, out)
	}
}

func TestHintsNarrowDown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := testRules("demo")
	r.Hints = rules.Hints{Count: 3, Per: rules.PerRound, Kind: rules.HintRegion}
	g, _ := f.svc.Create(ctx, f.user, "city", r)
	_, _ = f.svc.NextRound(ctx, f.user, g.ID)
	_, _ = f.svc.Shown(ctx, f.user, g.ID, 1, nil)
	var got []*Hint
	for range 3 {
		h, err := f.svc.Hint(ctx, f.user, g.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, h)
	}
	// the region (Tashkent) is the whole map: skipped; the district, then circles that halve
	if got[0].Kind != "district" || got[0].Place == "" || got[0].RadiusKm != 0 {
		t.Fatalf("first hint: %+v", got[0])
	}
	if got[1].Kind != "circle" || got[2].Kind != "circle" || got[2].RadiusKm != got[1].RadiusKm/2 {
		t.Fatalf("circles: %+v %+v", got[1], got[2])
	}
	if _, err := f.svc.Hint(ctx, f.user, g.ID, 1); !errors.Is(err, ErrNoHints) {
		t.Fatalf("a 4th hint: %v", err)
	}
}

func TestPlaceMapAndMovement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	m, err := f.svc.Catalog.Get(ctx, maps.PlaceMap("west"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Places) != 1 || m.Count(map[string]bool{"yandex": true}) != 20 {
		t.Fatalf("west's map: places %v, %d panoramas", m.Places, m.Count(map[string]bool{"yandex": true}))
	}
	if _, err := f.svc.Catalog.Get(ctx, maps.PlaceMap("atlantis")); !errors.Is(err, maps.ErrNotFound) {
		t.Fatalf("a place that does not exist: %v", err)
	}
	// demo panoramas cannot walk: Move plays as No move
	r := testRules("demo")
	r.Movement = rules.Move
	g, err := f.svc.Create(ctx, f.user, maps.PlaceMap("west"), r)
	if err != nil {
		t.Fatal(err)
	}
	if g.Rules.Movement != rules.NoMove {
		t.Fatalf("movement %s", g.Rules.Movement)
	}
}

func TestMyStreets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// every panorama in the west district is on one street, the rest on another
	if _, err := f.pool.Exec(ctx, `INSERT INTO streets (place_id, name_uz) SELECT id, 'G‘arb ko‘chasi' FROM places WHERE slug = 'west'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO streets (place_id, name_uz) SELECT id, 'Sharq ko‘chasi' FROM places WHERE slug = 'east'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE panoramas p SET street_id = s.id FROM streets s JOIN places pl ON pl.id = s.place_id
		WHERE ST_Contains(pl.geom, p.pos)`); err != nil {
		t.Fatal(err)
	}
	g, err := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	playRound(t, f, g.ID, 1)
	playRound(t, f, g.ID, 2)

	q := store.New(f.pool)
	rows, err := q.MyStreets(ctx, store.MyStreetsParams{UserID: f.user})
	if err != nil {
		t.Fatal(err)
	}
	rounds := 0
	for _, r := range rows {
		rounds += int(r.Rounds)
		if r.BestKm < 0 || r.PlaceUz == "" {
			t.Fatalf("street %q: best %v km, place %q", r.NameUz, r.BestKm, r.PlaceUz)
		}
	}
	if len(rows) == 0 || rounds != 2 {
		t.Fatalf("got %d streets over %d rounds, want the 2 rounds played", len(rows), rounds)
	}
	sp, err := q.StreetProgress(ctx, store.StreetProgressParams{UserID: f.user, LearnedKm: places.LearnedKm})
	if err != nil {
		t.Fatal(err)
	}
	if int(sp.Seen) != len(rows) {
		t.Fatalf("progress says %d streets seen, the list has %d", sp.Seen, len(rows))
	}
	if other, _ := q.MyStreets(ctx, store.MyStreetsParams{UserID: f.other}); len(other) != 0 {
		t.Fatalf("another player sees %d streets", len(other))
	}
	// my map: a district's streets are the rounds inside it, and they add up to all of them
	inPlace := 0
	for _, slug := range []string{"west", "east"} {
		in, err := q.MyStreets(ctx, store.MyStreetsParams{UserID: f.user, Place: &slug})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range in {
			inPlace += int(r.Rounds)
			if want := map[string]string{"west": "G‘arb ko‘chasi", "east": "Sharq ko‘chasi"}[slug]; r.NameUz != want {
				t.Fatalf("%s shows %q", slug, r.NameUz)
			}
		}
	}
	if inPlace != 2 {
		t.Fatalf("the districts hold %d rounds, want 2", inPlace)
	}
}

func TestCrowdIsOtherPlayersOnTheSamePanorama(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	g, err := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	var pano int64
	if err := f.pool.QueryRow(ctx, `SELECT panorama_id FROM rounds WHERE game_id = $1 AND n = 1`, g.ID).Scan(&pano); err != nil {
		t.Fatal(err)
	}

	// someone else's round on this panorama, guessed km away
	other := func(km float64, late bool) {
		t.Helper()
		u, err := store.New(f.pool).CreateUser(ctx, store.CreateUserParams{Name: "Ukki 01", Avatar: "🦉"})
		if err != nil {
			t.Fatal(err)
		}
		o, err := f.svc.Create(ctx, u.ID, "city", testRules("demo"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.NextRound(ctx, u.ID, o.ID); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE rounds SET panorama_id = $1 WHERE game_id = $2 AND n = 1`, pano, o.ID)
		exec(`INSERT INTO guesses (game_id, n, user_id, km, score, late, guessed_at) VALUES ($1, 1, $2, $3, 0, $4, now())`, o.ID, u.ID, km, late)
	}
	other(2, false)
	other(4, false)
	other(100, true) // late: not counted

	if c := f.svc.crowd(ctx, f.user, g.ID, 1); c != nil {
		t.Fatalf("two guesses are no crowd: %+v", c)
	}
	other(6, false)

	if _, err := f.svc.Shown(ctx, f.user, g.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Guess(ctx, f.user, g.ID, 1, &geo.Point{Lat: 41.3, Lon: 69.2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Crowd == nil || res.Crowd.Km != 4 || res.Crowd.N != 3 {
		t.Fatalf("crowd = %+v, want 4 km of 3", res.Crowd)
	}
	// a repeated guess gets the same, and the player's own guess never counts
	again, err := f.svc.Guess(ctx, f.user, g.ID, 1, &geo.Point{Lat: 41.3, Lon: 69.2})
	if err != nil {
		t.Fatal(err)
	}
	if again.Crowd == nil || again.Crowd.Km != 4 {
		t.Fatalf("repeated crowd = %+v", again.Crowd)
	}
}

func TestPlaceMapNeighboursShareOneBorder(t *testing.T) {
	f := setup(t)
	rows, err := store.New(f.pool).PlaceMapShapes(context.Background(), "toshkent")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d shapes, want the two districts", len(rows))
	}
	corners := func(d string) map[string]bool {
		out := map[string]bool{}
		for _, p := range regexp.MustCompile(`-?[\d.]+ -?[\d.]+`).FindAllString(d, -1) {
			out[p] = true
		}
		return out
	}
	a, b := corners(rows[0].D), corners(rows[1].D)
	shared := 0
	for p := range a {
		if b[p] {
			shared++
		}
	}
	if shared < 2 {
		t.Fatalf("the border isn't the same line: %q and %q", rows[0].D, rows[1].D)
	}
}

func TestAPlaceWithFewPanoramasPlaysThemAll(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE panoramas SET status = 'gone' WHERE provider = 'demo' AND ref NOT IN ('demo-0', 'demo-1')`); err != nil {
		t.Fatal(err)
	}
	g, err := f.svc.Create(ctx, f.user, "city", func() *rules.Rules { r := testRules("demo"); r.Rounds = 10; return r }())
	if err != nil {
		t.Fatalf("a place with two panoramas was refused: %v", err)
	}
	if g.Rules.Rounds != 2 {
		t.Fatalf("rounds = %d, want the 2 it has", g.Rules.Rounds)
	}
}

// playAll plays every round of a game by a player, guessing at pos, and returns the panoramas.
func playAll(t *testing.T, f *fixture, user, id uuid.UUID, rounds int, pos geo.Point) []int64 {
	t.Helper()
	ctx := context.Background()
	for n := 1; n <= rounds; n++ {
		if _, err := f.svc.NextRound(ctx, user, id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Shown(ctx, user, id, n, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Guess(ctx, user, id, n, &pos); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.New(f.pool).GameRounds(ctx, store.GameRoundsParams{GameID: id, UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.PanoramaID
	}
	return out
}

func TestChallengeIsTheSameStreetsForEveryone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	q := store.New(f.pool)
	g, err := f.svc.Create(ctx, f.user, "city", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateChallenge(ctx, f.user, g.ID); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("a challenge from a game still on: %v", err)
	}
	want := playAll(t, f, f.user, g.ID, 2, geo.Point{Lat: 41.3, Lon: 69.2})

	if _, err := f.svc.CreateChallenge(ctx, f.other, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a challenge from someone else's game: %v", err)
	}
	code, err := f.svc.CreateChallenge(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.svc.CreateChallenge(ctx, f.user, g.ID); err != nil || again != code {
		t.Fatalf("asked twice: %q and %q, %v", code, again, err)
	}

	// the challenger stands on the board from the start, and no one has a "you" yet
	ch, err := f.svc.GetChallenge(ctx, f.other, code)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Players != 1 || ch.Board[0].User.ID != f.user || ch.You != nil || ch.Rounds != 2 {
		t.Fatalf("a new challenge: %+v", ch)
	}

	// a friend, a guest, plays the same panoramas, the same way round
	mine, err := f.svc.PlayChallenge(ctx, f.other, code)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Challenge == nil || *mine.Challenge != code {
		t.Fatalf("the game doesn't know its challenge: %+v", mine.Challenge)
	}
	if again, err := f.svc.PlayChallenge(ctx, f.other, code); err != nil || again.ID != mine.ID {
		t.Fatalf("a second tap started another game: %v", err)
	}
	got := playAll(t, f, f.other, mine.ID, 2, geo.Point{Lat: 41.25, Lon: 69.15})
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("panoramas %v, the challenger had %v", got, want)
	}
	if again, err := f.svc.PlayChallenge(ctx, f.other, code); err != nil || again.ID != mine.ID || again.Status != "finished" {
		t.Fatalf("after the end the link gives the finished game: %+v, %v", again, err)
	}

	// both on one board, best first, the friend's place known
	ch, err = f.svc.GetChallenge(ctx, f.other, code)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Players != 2 || ch.You == nil || ch.You.Place == 0 || ch.Board[0].Total < ch.Board[1].Total {
		t.Fatalf("the board: %+v, you %+v", ch.Board, ch.You)
	}

	// the ring is the challenge's players, like a room's: two guesses are enough
	crowd := f.svc.crowd(ctx, f.other, mine.ID, 1)
	if crowd == nil || crowd.N != 2 {
		t.Fatalf("crowd = %+v, want the 2 players", crowd)
	}

	// the card's squares: the challenger's rounds
	scores, err := f.svc.CreatorScores(ctx, ch)
	if err != nil || len(scores) != 2 {
		t.Fatalf("scores %v, %v", scores, err)
	}

	// a merge keeps the link and leaves one row per player on the board
	for _, step := range []error{
		q.MergeGames(ctx, store.MergeGamesParams{UserID: f.other, OtherID: f.user}),
		q.MergeGuesses(ctx, store.MergeGuessesParams{UserID: f.other, OtherID: f.user}),
		q.MergeChallenges(ctx, store.MergeChallengesParams{UserID: f.other, OtherID: f.user}),
		q.DeleteUser(ctx, f.user),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	ch, err = f.svc.GetChallenge(ctx, f.other, code)
	if err != nil || ch.Players != 1 || ch.By.ID != f.other {
		t.Fatalf("after a merge: %+v, %v", ch, err)
	}
}

func TestChallengeFromAnEndlessGameIsTheRoundsPlayed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := testRules("demo")
	r.Rounds = 0
	g, err := f.svc.Create(ctx, f.user, "city", r)
	if err != nil {
		t.Fatal(err)
	}
	playAll(t, f, f.user, g.ID, 3, geo.Point{Lat: 41.3, Lon: 69.2})
	if _, err := f.svc.Finish(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	code, err := f.svc.CreateChallenge(ctx, f.user, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := f.svc.PlayChallenge(ctx, f.other, code)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Rules.Rounds != 3 {
		t.Fatalf("rounds %d, want the 3 played", mine.Rules.Rounds)
	}
	playAll(t, f, f.other, mine.ID, 3, geo.Point{Lat: 41.3, Lon: 69.2})
	if _, err := f.svc.NextRound(ctx, f.other, mine.ID); !errors.Is(err, ErrGameOver) {
		t.Fatalf("a fourth round: %v", err)
	}
}

func TestChallengeFromARoomPutsEveryoneOnTheBoard(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	roomGame := uuid.New()
	for _, u := range []uuid.UUID{f.user, f.other} {
		g, err := f.svc.Create(ctx, u, "city", testRules("demo"))
		if err != nil {
			t.Fatal(err)
		}
		playAll(t, f, u, g.ID, 2, geo.Point{Lat: 41.3, Lon: 69.2})
		if _, err := f.pool.Exec(ctx, `UPDATE games SET room_game = $1, mode = 'room' WHERE id = $2`, roomGame, g.ID); err != nil {
			t.Fatal(err)
		}
	}
	var first, second uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM games WHERE user_id = $1`, f.user).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT id FROM games WHERE user_id = $1`, f.other).Scan(&second); err != nil {
		t.Fatal(err)
	}
	code, err := f.svc.CreateChallenge(ctx, f.user, first)
	if err != nil {
		t.Fatal(err)
	}
	// the other player of the room gets the same link, and neither plays it twice
	if same, err := f.svc.CreateChallenge(ctx, f.other, second); err != nil || same != code {
		t.Fatalf("the room's other player: %q, %v", same, err)
	}
	ch, err := f.svc.GetChallenge(ctx, f.other, code)
	if err != nil || ch.Players != 2 || ch.You == nil || ch.You.Status != "finished" {
		t.Fatalf("the room's board: %+v, %v", ch, err)
	}
}
