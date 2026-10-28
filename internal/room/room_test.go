package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
	"github.com/hakim-134/api-geo-shafaq/internal/testdb"
)

// memStore is Valkey for tests.
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Save(_ context.Context, code string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[code] = append([]byte(nil), data...)
	return nil
}

func (s *memStore) Load(_ context.Context, code string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[code], nil
}

func (s *memStore) Delete(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, code)
	return nil
}

func (s *memStore) TTL() time.Duration { return 2 * time.Hour }

// page is a player's page: it keeps the last state it got.
type page struct {
	mu     sync.Mutex
	last   map[string]any
	msgs   int
	closed string
}

func (p *page) Send(msg []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs++
	_ = json.Unmarshal(msg, &p.last)
}

func (p *page) Close(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = reason
}

type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *memStore
	svc   *game.Service
	hub   *Hub
	clock time.Time
	users []Who
}

func setup(t *testing.T, players int) *fixture {
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
	f := &fixture{t: t, pool: pool, store: &memStore{m: map[string][]byte{}}, clock: time.Now()}
	f.svc = &game.Service{Pool: pool, Catalog: maps.NewCatalog(q, reg, &places.Cache{Q: q, Providers: reg.Names()}), Reg: reg, Usage: provider.Usage{Q: q}, Now: f.now}
	f.hub = f.newHub()
	for i := range players {
		u, err := q.CreateUser(ctx, store.CreateUserParams{Name: fmt.Sprintf("🐼_%02d", i), Avatar: "🐼"})
		if err != nil {
			t.Fatal(err)
		}
		f.users = append(f.users, Who{ID: u.ID, Name: u.Name, Avatar: u.Avatar, Guest: true})
	}
	return f
}

func (f *fixture) now() time.Time { return f.clock }

// newHub is a fresh process: nothing in memory, the same Valkey and Postgres.
func (f *fixture) newHub() *Hub {
	return &Hub{Games: f.svc, Pool: f.pool, Store: f.store, Now: f.now, Encode: func(v *View) []byte {
		b, _ := json.Marshal(map[string]any{"phase": v.Phase, "ver": v.Ver, "players": len(v.Players)})
		return b
	}}
}

func (f *fixture) tick(d time.Duration) {
	f.clock = f.clock.Add(d)
	f.hub.Tick(context.Background())
}

func roomRules(rounds, timeSec int) *rules.Rules {
	r := rules.Normal
	r.Rounds = rounds
	r.TimeSec = timeSec
	r.Hints = rules.Hints{Count: 1, Per: rules.PerGame, Kind: rules.HintDistrict, Cost: 0.5}
	r.Providers = []string{"demo"}
	return &r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// open is a room with every fixture user's page joined, the first one hosting.
func (f *fixture) open(rs *rules.Rules) (*Room, []*page) {
	f.t.Helper()
	ctx := context.Background()
	rm, err := f.hub.Create(ctx, f.users[0], "city", rs)
	must(f.t, err)
	var pages []*page
	for _, u := range f.users {
		p := &page{}
		must(f.t, rm.Join(ctx, p, u))
		pages = append(pages, p)
	}
	return rm, pages
}

func TestRoomPlaysAGameAndEveryoneKeepsIt(t *testing.T) {
	f := setup(t, 3)
	ctx := context.Background()
	rm, pages := f.open(roomRules(2, 60))
	a, b, c := f.users[0].ID, f.users[1].ID, f.users[2].ID

	if err := rm.Start(ctx, b); !errors.Is(err, ErrNotHost) {
		t.Fatalf("a player who is not the host started the game: %v", err)
	}
	must(t, rm.Start(ctx, a))
	v := rm.View(b)
	if v.Phase != Playing || v.Round == nil || v.Round.N != 1 || v.Round.View.ImageURL == "" {
		t.Fatalf("round 1 should be on, with a panorama: %+v", v.Round)
	}
	if pages[1].last["phase"] != "playing" {
		t.Fatalf("the pages were not told: %v", pages[1].last)
	}

	// everyone opens it; the first starts the 60 s clock
	for _, id := range []uuid.UUID{a, b, c} {
		must(t, rm.Shown(ctx, id, 1, nil))
	}
	if r := rm.View(a).Round.Remaining; r == nil || *r != 60*time.Second {
		t.Fatalf("the clock should start at 60 s: %v", r)
	}

	// a guesses, b only places a pin, c does nothing: after the first answer the others
	// have 45 s, then the round ends with b's pin and nothing for c
	actual := rm.st.Round.Actual
	near := geo.Offset(actual, 0.3, 1)
	must(t, rm.Guess(ctx, a, 1, &actual))
	if r := rm.View(b).Round.Remaining; r == nil || *r != afterFirstGuess {
		t.Fatalf("after the first guess the others get 45 s: %v", r)
	}
	must(t, rm.Pin(ctx, b, 1, &near))
	if _, err := rm.Hint(ctx, a, 1); !errors.Is(err, game.ErrWrongPhase) {
		t.Fatalf("a hint after guessing: %v", err)
	}
	h, err := rm.Hint(ctx, c, 1)
	must(t, err)
	if h.Kind != "district" {
		t.Fatalf("the hint should name the district: %+v", h)
	}
	if _, err := rm.Hint(ctx, c, 1); !errors.Is(err, game.ErrNoHints) {
		t.Fatalf("a second hint of one per game: %v", err)
	}

	f.tick(30 * time.Second)
	if rm.View(a).Phase != Playing {
		t.Fatal("the round ended before its time")
	}
	f.tick(20 * time.Second)
	v = rm.View(a)
	if v.Phase != Result || len(v.Results) != 1 {
		t.Fatalf("the round should have ended at its deadline: %s", v.Phase)
	}
	scores := map[uuid.UUID]Score{}
	for _, s := range v.Results[0].Scores {
		scores[s.Player] = s
	}
	if scores[a].Score != 5000 || scores[b].Score == 0 || scores[b].Score >= 5000 || scores[c].Score != 0 || scores[c].Hints != 1 {
		t.Fatalf("scores: a perfect, b by the pin, c nothing with a hint: %+v", scores)
	}

	if err := rm.Next(ctx, b, false); !errors.Is(err, ErrNotHost) {
		t.Fatalf("only the host goes on: %v", err)
	}
	must(t, rm.Next(ctx, a, false))
	for _, id := range []uuid.UUID{a, b, c} {
		must(t, rm.Shown(ctx, id, 2, nil))
	}
	// when everyone answered, the round ends at once
	far := geo.Point{Lat: 41.25, Lon: 69.15}
	must(t, rm.Guess(ctx, a, 2, &far))
	must(t, rm.Guess(ctx, b, 2, &far))
	must(t, rm.Guess(ctx, b, 2, &actual)) // a second answer changes nothing
	must(t, rm.Guess(ctx, c, 2, nil))
	if rm.View(a).Phase != Result {
		t.Fatal("the round should end when everyone answered")
	}
	must(t, rm.Next(ctx, a, false))
	v = rm.View(c)
	if v.Phase != Final || len(v.Results) != 2 {
		t.Fatalf("after the last round the table: %s", v.Phase)
	}

	// every player's game is in Postgres: finished, both rounds, the same total
	q := store.New(f.pool)
	for i, p := range v.Players {
		gameID, ok := rm.st.GameRows[p.ID]
		if !ok {
			t.Fatalf("player %d has no game", i)
		}
		g, err := f.svc.Get(ctx, p.ID, gameID)
		must(t, err)
		if g.Status != "finished" || len(g.Rounds) != 2 || g.Total != p.Total || g.RoomGame == nil || *g.RoomGame != rm.st.GameID {
			t.Fatalf("player %d's game: %s, %d rounds, total %d (room %d)", i, g.Status, len(g.Rounds), g.Total, p.Total)
		}
		hist, err := q.ListUserGames(ctx, store.ListUserGamesParams{UserID: p.ID, Lim: 5})
		must(t, err)
		if len(hist) != 1 || hist[0].Players != 3 {
			t.Fatalf("player %d's history should hold the room game with 3 players: %+v", i, hist)
		}
	}
	table, err := q.RoomGameTable(ctx, &rm.st.GameID)
	must(t, err)
	if len(table) != 3 || table[0].Total < table[2].Total {
		t.Fatalf("the room table: %+v", table)
	}

	// a rematch: a new game, totals back to 0
	must(t, rm.Start(ctx, a))
	if v := rm.View(a); v.Games != 2 || v.Players[0].Total != 0 || len(v.Results) != 0 {
		t.Fatalf("a rematch starts from zero: %+v", v)
	}
}

func TestRoomSurvivesARestart(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, _ := f.open(roomRules(2, 60))
	a, b := f.users[0].ID, f.users[1].ID
	must(t, rm.Start(ctx, a))
	must(t, rm.Shown(ctx, a, 1, nil))
	actual := rm.st.Round.Actual
	must(t, rm.Pin(ctx, b, 1, &actual))
	f.tick(10 * time.Second)

	// the process goes away (a deploy); a new one starts with nothing in memory
	f.hub.Shutdown(ctx)
	code := rm.st.Code
	f.hub = f.newHub()
	f.clock = f.clock.Add(5 * time.Second)

	rm2, err := f.hub.Get(ctx, "  "+code[:2]+code[2:]+" ")
	must(t, err)
	v := rm2.View(b)
	if v.Phase != Playing || v.Round == nil || v.Round.N != 1 || v.Round.Pin == nil || *v.Round.Pin != actual {
		t.Fatalf("the round and b's pin should come back: %+v", v.Round)
	}
	if r := v.Round.Remaining; r == nil || *r != 45*time.Second {
		t.Fatalf("the clock goes on from its deadline (60 − 15 s): %v", r)
	}
	// the pages come back within awayGrace: the round does not end without them
	pa, pb := &page{}, &page{}
	f.tick(3 * time.Second)
	must(t, rm2.Join(ctx, pa, f.users[0]))
	must(t, rm2.Join(ctx, pb, f.users[1]))
	if rm2.View(a).Phase != Playing {
		t.Fatal("the round ended during the restart")
	}
	must(t, rm2.Guess(ctx, a, 1, &actual))
	must(t, rm2.Guess(ctx, b, 1, &actual))
	if v := rm2.View(a); v.Phase != Result || v.Results[0].Scores[1].Score != 5000 {
		t.Fatalf("the round should finish after the restart: %+v", v.Results)
	}
	must(t, rm2.Next(ctx, a, true))
	g, err := f.svc.Get(ctx, b, rm2.st.GameRows[b])
	must(t, err)
	if g.Status != "finished" || g.Total != 5000 {
		t.Fatalf("b's game after the restart: %s %d", g.Status, g.Total)
	}
}

func TestAPlayerWhoIsGoneIsWaitedForAMoment(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, pages := f.open(roomRules(1, 0))
	a, b := f.users[0].ID, f.users[1].ID
	must(t, rm.Start(ctx, a))
	must(t, rm.Shown(ctx, a, 1, nil))
	rm.Drop(ctx, pages[1]) // b reloads the page
	pos := geo.Point{Lat: 41.3, Lon: 69.2}
	must(t, rm.Guess(ctx, a, 1, &pos))
	f.tick(5 * time.Second)
	if rm.View(a).Phase != Playing {
		t.Fatal("the round did not wait for b's reload")
	}
	f.tick(awayGrace)
	if rm.View(a).Phase != Result {
		t.Fatal("the round should go on without a player gone for long")
	}
	// b never had a page open in this round's end... but did at its start: b's 0 is kept
	if _, ok := rm.st.GameRows[b]; !ok {
		t.Fatal("b was in the round: b's game should be saved")
	}
}

func TestHostToolsAndLimits(t *testing.T) {
	f := setup(t, MaxPlayers+1)
	ctx := context.Background()
	rm, err := f.hub.Create(ctx, f.users[0], "city", roomRules(1, 0))
	must(t, err)
	pages := map[uuid.UUID]*page{}
	for i, u := range f.users {
		p := &page{}
		err := rm.Join(ctx, p, u)
		if i < MaxPlayers {
			must(t, err)
			pages[u.ID] = p
		} else if !errors.Is(err, ErrFull) {
			t.Fatalf("player 13 joined: %v", err)
		}
	}
	host, b, c := f.users[0].ID, f.users[1].ID, f.users[2].ID

	if err := rm.Kick(ctx, b, c); !errors.Is(err, ErrNotHost) {
		t.Fatalf("a player kicked another: %v", err)
	}
	must(t, rm.Kick(ctx, host, c))
	if pages[c].closed != "kicked" {
		t.Fatal("the kicked player's page should be told")
	}
	if err := rm.Check(c); !errors.Is(err, ErrKicked) {
		t.Fatalf("a kicked player came back: %v", err)
	}
	// a seat is free now
	must(t, rm.Join(ctx, &page{}, f.users[MaxPlayers]))

	must(t, rm.MakeHost(ctx, host, b))
	if rm.View(host).Host != b {
		t.Fatal("the host should be b")
	}
	// b changes the rules between games
	rs := roomRules(3, 90)
	must(t, rm.SetRules(ctx, b, "city", rs))
	if v := rm.View(b); v.Rules.Rounds != 3 || v.Rules.TimeSec != 90 {
		t.Fatalf("the rules did not change: %+v", v.Rules)
	}
	if err := rm.SetRules(ctx, b, "city", &rules.Rules{Rounds: 99}); !errors.Is(err, game.ErrBadInput) {
		t.Fatalf("bad rules were taken: %v", err)
	}
	// a host gone for good hands the room on
	rm.Drop(ctx, pages[b])
	f.tick(5 * time.Second)
	if rm.View(host).Host != b {
		t.Fatal("a reload is not a handover")
	}
	f.tick(awayGrace)
	if rm.View(host).Host == b {
		t.Fatal("the room should pass to a player who is here")
	}
	// leaving between games frees the seat
	must(t, rm.Quit(ctx, f.users[3].ID))
	if rm.player(f.users[3].ID) != nil {
		t.Fatal("a player who left in the lobby keeps the seat")
	}
	if err := rm.Pin(ctx, f.users[3].ID, 1, nil); !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("someone not in the room acted: %v", err)
	}
}

func TestAPanoramaNobodyCanOpenIsSwappedOnce(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, _ := f.open(roomRules(1, 0))
	a, b := f.users[0].ID, f.users[1].ID
	must(t, rm.Start(ctx, a))
	first := rm.st.Round.PanoID
	must(t, rm.Fail(ctx, a, 1, 0, true))
	must(t, rm.Fail(ctx, b, 1, 0, true)) // the same panorama, failing on b's page too
	if rd := rm.st.Round; rd.Swap != 1 || rd.PanoID == first {
		t.Fatalf("one swap for two pages failing at once: swap %d", rd.Swap)
	}
	must(t, rm.Shown(ctx, b, 1, nil))
	if err := rm.Fail(ctx, a, 1, 1, false); !errors.Is(err, game.ErrWrongPhase) {
		t.Fatalf("once someone sees it, it stays: %v", err)
	}
}

func TestAnIdleRoomCloses(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, pages := f.open(roomRules(2, 0))
	a := f.users[0].ID
	must(t, rm.Start(ctx, a))
	must(t, rm.Guess(ctx, a, 1, nil))
	must(t, rm.Guess(ctx, f.users[1].ID, 1, nil))
	for _, p := range pages {
		rm.Drop(ctx, p)
	}
	f.tick(idleTTL + time.Second)
	if _, err := f.hub.Get(ctx, rm.st.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an idle room should be gone: %v", err)
	}
	g, err := f.svc.Get(ctx, a, rm.st.GameRows[a])
	must(t, err)
	if g.Status != "finished" {
		t.Fatalf("its game should be finished with the round played: %s", g.Status)
	}
}

func TestTheNextRoundStartsByItself(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, pages := f.open(roomRules(3, 0))
	a, b := f.users[0].ID, f.users[1].ID
	play := func(rm *Room, n int) {
		t.Helper()
		must(t, rm.Shown(ctx, a, n, nil))
		pos := geo.Offset(rm.st.Round.Actual, 0.5, 1)
		must(t, rm.Guess(ctx, a, n, &pos))
		must(t, rm.Guess(ctx, b, n, &pos))
		if rm.View(a).Phase != Result {
			t.Fatalf("round %d should have ended", n)
		}
	}
	must(t, rm.Start(ctx, a))

	// b's hint costs half the round (roomRules): the result says what it was before it
	must(t, rm.Shown(ctx, a, 1, nil))
	_, err := rm.Hint(ctx, b, 1)
	must(t, err)
	pos := geo.Offset(rm.st.Round.Actual, 0.5, 1)
	must(t, rm.Guess(ctx, a, 1, &pos))
	must(t, rm.Guess(ctx, b, 1, &pos))
	v := rm.View(a)
	sa, sb := v.Results[0].Scores[0], v.Results[0].Scores[1]
	if sa.Base != 0 || sb.Base != sa.Score || sb.Score != (sa.Score+1)/2 || sb.Hints != 1 {
		t.Fatalf("the same guess: a without a hint, b with one at half: %+v %+v", sa, sb)
	}

	// the reveal, then a 10 s ring, then round 2 for everyone
	if v.NextIn == nil || *v.NextIn != revealFor+autoNext {
		t.Fatalf("a result goes on by itself in 13 s: %v", v.NextIn)
	}
	f.tick(12 * time.Second)
	if v := rm.View(b); v.Phase != Result || *v.NextIn != time.Second {
		t.Fatalf("the ring is still on: %s %v", v.Phase, v.NextIn)
	}
	f.tick(time.Second)
	if v := rm.View(b); v.Phase != Playing || v.Round.N != 2 || v.NextIn != nil {
		t.Fatalf("round 2 should start by itself: %s", v.Phase)
	}
	if pages[1].last["phase"] != "playing" {
		t.Fatalf("the pages were not told: %v", pages[1].last)
	}

	// the host holds it: the room waits for their Next
	play(rm, 2)
	if err := rm.Hold(ctx, b); !errors.Is(err, ErrNotHost) {
		t.Fatalf("only the host holds: %v", err)
	}
	must(t, rm.Hold(ctx, a))
	f.tick(30 * time.Second)
	if v := rm.View(b); v.Phase != Result || v.NextIn != nil {
		t.Fatalf("a held result waits: %s %v", v.Phase, v.NextIn)
	}
	must(t, rm.Next(ctx, a, false))

	// a restart in the middle of the ring: it goes on from where it was, then to the table
	play(rm, 3)
	f.tick(5 * time.Second)
	f.hub.Shutdown(ctx)
	code := rm.st.Code
	f.hub = f.newHub()
	f.clock = f.clock.Add(4 * time.Second)
	rm2, err := f.hub.Get(ctx, code)
	must(t, err)
	if v := rm2.View(a); v.Phase != Result || v.NextIn == nil || *v.NextIn != 4*time.Second {
		t.Fatalf("the ring goes on after a restart (13 − 9 s): %s %v", v.Phase, v.NextIn)
	}
	f.tick(4 * time.Second)
	if v := rm2.View(a); v.Phase != Final {
		t.Fatalf("after the last round's ring, the table: %s", v.Phase)
	}
}

func TestAnEmptyRoomDoesNotPlayOn(t *testing.T) {
	f := setup(t, 2)
	ctx := context.Background()
	rm, pages := f.open(roomRules(3, 0))
	a, b := f.users[0].ID, f.users[1].ID
	must(t, rm.Start(ctx, a))
	must(t, rm.Shown(ctx, a, 1, nil))
	must(t, rm.Guess(ctx, a, 1, nil))
	must(t, rm.Guess(ctx, b, 1, nil))
	rm.Drop(ctx, pages[0])
	rm.Drop(ctx, pages[1])
	f.tick(awayGrace)
	f.tick(time.Second)
	if v := rm.View(a); v.Phase != Result || v.NextIn != nil {
		t.Fatalf("nobody is there: the room waits, and spends no panorama: %s %v", v.Phase, v.NextIn)
	}
}
