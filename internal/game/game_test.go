package game

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
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
	f.svc = &Service{Pool: pool, Catalog: maps.NewCatalog(q, reg), Reg: reg, Usage: provider.Usage{Q: q}, Now: func() time.Time { return f.clock }}
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
	r := rules.Normal
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
	if first.Score != 2500 {
		t.Fatalf("a perfect guess with one hint at 50%% should be 2500, got %d", first.Score)
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
