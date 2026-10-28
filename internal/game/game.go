// Package game runs solo games on the server: it picks every round's panorama, keeps the
// clock and scores the guesses. The browser only shows what it is sent.
package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/score"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Errors carry the stable codes the API returns.
var (
	ErrNotFound     = errors.New("not_found")
	ErrBadInput     = errors.New("bad_input")
	ErrWrongPhase   = errors.New("wrong_phase")
	ErrGameOver     = errors.New("game_over")
	ErrNoPanorama   = errors.New("no_panorama")
	ErrDailyCap     = errors.New("daily_cap")
	ErrTooFar       = errors.New("too_far")
	ErrNoHints      = errors.New("no_hints")
	ErrTooManyFails = errors.New("too_many_fails")
)

const (
	// The clock starts when the panorama is on screen, but at the latest this long after the
	// round was handed out, so nobody can study a panorama before "showing" it.
	showWindow = 60 * time.Second
	// A guess sent right at the deadline still counts (the network takes a moment).
	guessGrace = 3 * time.Second
	// How many times a round may swap a panorama the viewer could not open.
	maxReplace = 5
	// A Yandex panorama may be a little further from the search point than the map's snap
	// distance; the JS API rounds positions.
	snapSlackKm = 0.02
)

type Service struct {
	Pool    *pgxpool.Pool
	Catalog *maps.Catalog
	Reg     *provider.Registry
	Usage   provider.Usage
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Game is a game as the player sees it.
type Game struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Map        *maps.Map
	Rules      rules.Rules
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Total      int
	HintsUsed  int
	Rounds     []Round
	RoomCode   *string    // a room game: the room's code
	RoomGame   *uuid.UUID // and the room game, shared by every player's row of it
}

// Max is the best possible total: of the rounds played so far, in an endless game.
func (g *Game) Max() int {
	if !g.Rules.Endless() {
		return g.Rules.Rounds * score.Max
	}
	n := 0
	for _, r := range g.Rounds {
		if r.Result != nil {
			n++
		}
	}
	return n * score.Max
}

// HintsLeft in the game, for per-game hint rules.
func (g *Game) HintsLeft() int { return g.Rules.HintsLeft(0, g.HintsUsed) }

type Round struct {
	N         int
	View      provider.View
	ShownAt   *time.Time
	Deadline  *time.Time
	Remaining *time.Duration
	Hints     []Hint
	HintsLeft int
	Result    *Result
}

// Hint names the region or district (Place is its slug, for its outline on the map), or
// draws a circle that holds the place (RadiusKm > 0). Hints from before PLAN 4.4 do both.
type Hint struct {
	Kind     string      `json:"kind"` // region | district | circle
	Text     places.Text `json:"text"`
	Place    string      `json:"place,omitempty"`
	Center   geo.Point   `json:"center"`
	RadiusKm float64     `json:"radiusKm"`
}

type Result struct {
	Actual    geo.Point
	Guess     *geo.Point
	Km        *float64
	Score     int
	Base      int // the score before hints, when they took points (0: they took none)
	HintsUsed int
	Late      bool
	Place     places.Info
	GameOver  bool
	Total     int
}

// Create starts a solo game on a map. r nil = the map's default rules.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, slug string, r *rules.Rules) (*Game, error) {
	m, rs, err := s.Prepare(ctx, slug, r)
	if err != nil {
		return nil, err
	}
	rulesJSON, _ := json.Marshal(rs)
	row, err := store.New(s.Pool).CreateGame(ctx, store.CreateGameParams{
		UserID: userID, MapID: m.ID, Rules: rulesJSON, SizeKm: m.SizeKm, Seed: rand.Int64(),
	})
	if err != nil {
		return nil, err
	}
	metrics.GamesStarted.WithLabelValues(m.Slug).Inc()
	return &Game{
		ID: row.ID, UserID: userID, Map: m, Rules: rs, Status: row.Status,
		StartedAt: row.StartedAt, Rounds: []Round{},
	}, nil
}

// Prepare checks a map and rules for a new game, solo or in a room: the map exists, the
// rules are valid (r nil = the map's own) and its panoramas are enough for them today.
func (s *Service) Prepare(ctx context.Context, slug string, r *rules.Rules) (*maps.Map, rules.Rules, error) {
	m, err := s.Catalog.Get(ctx, slug)
	if errors.Is(err, maps.ErrNotFound) {
		return nil, rules.Rules{}, ErrNotFound
	}
	if err != nil {
		return nil, rules.Rules{}, err
	}
	rs := m.Rules
	if r != nil {
		rs = *r
	}
	if rs.Providers == nil {
		rs.Providers = []string{}
	}
	if err := rs.Validate(s.Reg.Known()); err != nil {
		return nil, rs, ErrBadInput
	}
	allowed := s.Reg.Allowed(rs.Providers)
	if rs.Movement == rules.Move && !s.Reg.CanMove(allowed) {
		rs.Movement = rules.NoMove // offered only where the panoramas can do it
	}
	need := max(rs.Rounds, 1) // an endless game needs one to start
	if m.Count(allowed) < need {
		return nil, rs, ErrNoPanorama
	}
	open, err := s.uncapped(ctx, allowed)
	if err != nil {
		return nil, rs, err
	}
	if m.Count(open) < need {
		return nil, rs, ErrDailyCap
	}
	return m, rs, nil
}

// Get returns a game with its rounds. A running game is only its player's; a finished one
// can be opened by anyone with the link.
func (s *Service) Get(ctx context.Context, userID uuid.UUID, id uuid.UUID) (*Game, error) {
	q := store.New(s.Pool)
	row, err := q.GetGame(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.UserID != userID && row.Status == "playing" {
		return nil, ErrNotFound
	}
	return s.load(ctx, q, row, true)
}

// load turns stored rows into a Game. withPlaces adds "you were here" to finished rounds.
func (s *Service) load(ctx context.Context, q *store.Queries, row store.Game, withPlaces bool) (*Game, error) {
	m, err := s.Catalog.ByID(ctx, row.MapID)
	if err != nil {
		return nil, err
	}
	g := &Game{
		ID: row.ID, UserID: row.UserID, Map: m, Status: row.Status, StartedAt: row.StartedAt,
		FinishedAt: row.FinishedAt, Total: int(row.Total), HintsUsed: int(row.HintsUsed),
		RoomCode: row.RoomCode, RoomGame: row.RoomGame,
	}
	if err := json.Unmarshal(row.Rules, &g.Rules); err != nil {
		return nil, fmt.Errorf("game %s rules: %w", row.ID, err)
	}
	rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: row.ID, UserID: row.UserID})
	if err != nil {
		return nil, err
	}
	g.Rounds = make([]Round, 0, len(rows))
	for _, r := range rows {
		rd, err := s.round(g, r)
		if err != nil {
			return nil, err
		}
		if rd.Result != nil && withPlaces {
			if rd.Result.Place, err = places.Lookup(ctx, q, rd.Result.Actual); err != nil {
				return nil, err
			}
		}
		g.Rounds = append(g.Rounds, rd)
	}
	return g, nil
}

func (s *Service) round(g *Game, r store.GameRoundsRow) (Round, error) {
	rd := Round{N: int(r.N), ShownAt: r.ShownAt, Deadline: r.Deadline}
	if err := json.Unmarshal(orEmpty(r.Hints), &rd.Hints); err != nil {
		return rd, err
	}
	rd.HintsLeft = g.Rules.HintsLeft(len(rd.Hints), g.HintsUsed)
	if p, ok := s.Reg.Get(r.Provider); ok {
		pano := provider.Pano{ID: r.PanoramaID, Provider: r.Provider, Ref: r.PanoRef}
		rd.View = p.View(pano, geo.Point{Lat: r.NearLat, Lon: r.NearLon}, r.SnapKm, float64(r.Heading))
	} else {
		rd.View = provider.View{Provider: r.Provider} // disabled since; the game can still be read
	}
	if r.GuessedAt != nil {
		res := &Result{
			Actual:    geo.Point{Lat: r.ActualLat, Lon: r.ActualLon},
			Km:        r.Km,
			Score:     int(deref(r.Score)),
			HintsUsed: len(rd.Hints),
			Late:      deref(r.Late),
			GameOver:  g.Rules.Last(int(r.N)),
		}
		if r.HasGuess {
			res.Guess = &geo.Point{Lat: r.GuessLat, Lon: r.GuessLon}
		}
		if r.Km != nil && !res.Late {
			// the score before hints is not stored: the same formula gives it back, unless
			// the map's size changed since
			if sc, base := scored(*r.Km, g.Map.SizeKm, g.Rules, len(rd.Hints)); sc == res.Score {
				res.Base = base
			}
		}
		rd.Result = res
	} else if r.Deadline != nil {
		left := max(r.Deadline.Sub(s.now()), 0)
		rd.Remaining = &left
	}
	return rd, nil
}

// CostlyHints is hints taken in a game with these stored rules, or 0 when hints were free:
// a free hint has no cost to show.
func CostlyHints(rulesJSON []byte, hints int) int {
	var rs rules.Rules
	if hints == 0 || json.Unmarshal(rulesJSON, &rs) != nil || rs.Hints.Cost <= 0 {
		return 0
	}
	return hints
}

// scored is a guess km away with hints taken, and the score before them when they took points.
func scored(km, sizeKm float64, rs rules.Rules, hints int) (got, base int) {
	before := score.Round(km, sizeKm, rs.ScoreScale)
	got = score.WithHints(before, hints, rs.Hints.Cost)
	if got != before {
		base = before
	}
	return got, base
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func orEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("[]")
	}
	return b
}

// inGame runs fn with the game's row locked, so two taps cannot start two rounds or score a
// guess twice. Only the game's own player may change it.
func (s *Service) inGame(ctx context.Context, userID, id uuid.UUID, fn func(q *store.Queries, g *Game) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.LockGame(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.UserID != userID {
			return ErrNotFound
		}
		g, err := s.load(ctx, q, row, false)
		if err != nil {
			return err
		}
		return fn(q, g)
	})
}

// current is the round being played: the last one, not yet guessed.
func (g *Game) current(n int) (*Round, error) {
	if len(g.Rounds) == 0 || g.Rounds[len(g.Rounds)-1].N != n {
		return nil, ErrWrongPhase
	}
	rd := &g.Rounds[len(g.Rounds)-1]
	if rd.Result != nil {
		return nil, ErrWrongPhase
	}
	return rd, nil
}

// rng is the game's random source for one decision, so a seed always gives the same game.
func rng(seed int64, n, salt int) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), uint64(n)<<16|uint64(salt)))
}

// NextRound starts the next round, or returns the one already running.
func (s *Service) NextRound(ctx context.Context, userID, id uuid.UUID) (*Round, error) {
	var out *Round
	var counted string
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		if g.Status != "playing" {
			return ErrGameOver
		}
		if n := len(g.Rounds); n > 0 && g.Rounds[n-1].Result == nil {
			out = &g.Rounds[n-1]
			return nil
		}
		if g.Rules.Last(len(g.Rounds)) {
			return ErrGameOver
		}
		n := len(g.Rounds) + 1
		seed, err := s.seed(ctx, q, id)
		if err != nil {
			return err
		}
		c, near, heading, err := s.pick(ctx, q, g, seed, n, 0, nil)
		if errors.Is(err, ErrNoPanorama) && g.Rules.Endless() && n > 1 {
			// an endless game that has shown every panorama of its map is done
			if err := q.FinishGame(ctx, id); err != nil {
				return err
			}
			return ErrGameOver
		}
		if err != nil {
			return err
		}
		err = q.CreateRound(ctx, store.CreateRoundParams{
			GameID: id, N: int32(n), PanoramaID: c.ID, Provider: c.Provider,
			NearLat: near.Lat, NearLon: near.Lon, SnapKm: g.Map.Pick.SnapKm, Heading: float32(heading),
			CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		p, _ := s.Reg.Get(c.Provider)
		out = &Round{
			N: n, View: p.View(c.Pano, near, g.Map.Pick.SnapKm, heading),
			HintsLeft: g.Rules.HintsLeft(0, g.HintsUsed), Hints: []Hint{},
		}
		counted = c.Provider
		return nil
	})
	if err != nil {
		return nil, err
	}
	if counted != "" {
		s.Usage.Count(ctx, counted)
	}
	return out, nil
}

func (s *Service) seed(ctx context.Context, q *store.Queries, id uuid.UUID) (int64, error) {
	row, err := q.GetGame(ctx, id)
	return row.Seed, err
}

// uncapped is allowed without the providers that reached today's cap.
func (s *Service) uncapped(ctx context.Context, allowed map[string]bool) (map[string]bool, error) {
	capped, err := s.Usage.Capped(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for name := range allowed {
		if !capped[name] {
			out[name] = true
		}
	}
	return out, nil
}

// pick chooses a panorama for round n that earlier rounds did not show. skip is a panorama
// that just failed to open.
func (s *Service) pick(ctx context.Context, q *store.Queries, g *Game, seed int64, n, salt int, skip *int64) (maps.Candidate, geo.Point, float64, error) {
	rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: g.ID, UserID: g.UserID})
	if err != nil {
		return maps.Candidate{}, geo.Point{}, 0, err
	}
	used := maps.Used{IDs: map[int64]bool{}}
	for _, r := range rows {
		used.IDs[r.PanoramaID] = true
		if int(r.N) != n {
			used.Pos = append(used.Pos, geo.Point{Lat: r.ActualLat, Lon: r.ActualLon})
		}
	}
	if skip != nil {
		used.IDs[*skip] = true
	}
	p, err := s.PickRound(ctx, g.Map, g.Rules, used, seed, n, salt)
	return p.Cand, p.Near, p.Heading, err
}

// Pick is a round's panorama: what to show, where the viewer searches, where it looks first.
type Pick struct {
	Cand    maps.Candidate
	Near    geo.Point
	Heading float64
	View    provider.View
}

// PickRound chooses a panorama for round n of a game on m (solo or room) that used does not
// hold. Providers at today's cap give way to the game's others; when all of them are at it,
// the game still finishes on them (only new games are turned away).
func (s *Service) PickRound(ctx context.Context, m *maps.Map, rs rules.Rules, used maps.Used, seed int64, n, salt int) (Pick, error) {
	allowed := s.Reg.Allowed(rs.Providers)
	open, err := s.uncapped(ctx, allowed)
	if err != nil {
		return Pick{}, err
	}
	r := rng(seed, n, salt)
	c, ok := m.PickRound(r, open, used)
	if !ok {
		c, ok = m.PickRound(r, allowed, used)
	}
	if !ok {
		return Pick{}, ErrNoPanorama
	}
	near := m.Near(r, c)
	heading := math.Floor(r.Float64() * 360)
	out := Pick{Cand: c, Near: near, Heading: heading}
	if p, ok := s.Reg.Get(c.Provider); ok {
		out.View = p.View(c.Pano, near, m.Pick.SnapKm, heading)
	}
	return out, nil
}

// SnapCheck is where a round is scored, given the panorama the viewer reports it opened:
// for providers that search near a point it must be close to that point; others are scored
// against the library position.
func (s *Service) SnapCheck(provider string, near, library geo.Point, snapKm float64, pos *geo.Point) (geo.Point, error) {
	p, ok := s.Reg.Get(provider)
	if !ok {
		return geo.Point{}, ErrNoPanorama
	}
	if !p.Capabilities().SearchNear {
		return library, nil
	}
	if pos == nil || !pos.Valid() {
		return geo.Point{}, ErrBadInput
	}
	if geo.DistanceKm(*pos, near) > snapKm+snapSlackKm {
		return geo.Point{}, ErrTooFar
	}
	return *pos, nil
}

// Shown starts the round's clock. For providers that search near a point, pos is the
// panorama the viewer opened; it must be close to the point it was sent.
func (s *Service) Shown(ctx context.Context, userID, id uuid.UUID, n int, pos *geo.Point) (*Round, error) {
	var out *Round
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		rd, err := g.current(n)
		if err != nil {
			return err
		}
		if rd.ShownAt != nil {
			out = rd
			return nil
		}
		rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: id, UserID: userID})
		if err != nil {
			return err
		}
		row := rows[len(rows)-1]
		library := geo.Point{Lat: row.ActualLat, Lon: row.ActualLon}
		near := geo.Point{Lat: row.NearLat, Lon: row.NearLon}
		actual, err := s.SnapCheck(rd.View.Provider, near, library, row.SnapKm, pos)
		if err != nil {
			return err
		}
		now := s.now()
		start := now
		if latest := row.CreatedAt.Add(showWindow); now.After(latest) {
			start = latest
		}
		var deadline *time.Time
		if g.Rules.TimeSec > 0 {
			d := start.Add(time.Duration(g.Rules.TimeSec) * time.Second)
			deadline = &d
		}
		err = q.ShowRound(ctx, store.ShowRoundParams{
			GameID: id, N: int32(n), ShownAt: &now, Deadline: deadline, ActualLat: actual.Lat, ActualLon: actual.Lon,
		})
		if err != nil {
			return err
		}
		if err := q.PanoramaOpened(ctx, row.PanoramaID); err != nil {
			return err
		}
		rd.ShownAt, rd.Deadline = &now, deadline
		if deadline != nil {
			left := max(deadline.Sub(now), 0)
			rd.Remaining = &left
		}
		out = rd
		return nil
	})
	return out, err
}

// Failed swaps the round's panorama for another when the viewer could not open it.
// notFound (the provider has nothing there any more) counts against the panorama.
func (s *Service) Failed(ctx context.Context, userID, id uuid.UUID, n int, notFound bool) (*Round, error) {
	var out *Round
	var counted string
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		rd, err := g.current(n)
		if err != nil {
			return err
		}
		if rd.ShownAt != nil {
			return ErrWrongPhase
		}
		rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: id, UserID: userID})
		if err != nil {
			return err
		}
		row := rows[len(rows)-1]
		if row.Replaced >= maxReplace {
			return ErrTooManyFails
		}
		if notFound {
			if err := q.PanoramaFailed(ctx, row.PanoramaID); err != nil {
				return err
			}
		}
		seed, err := s.seed(ctx, q, id)
		if err != nil {
			return err
		}
		c, near, heading, err := s.pick(ctx, q, g, seed, n, int(row.Replaced)+1, &row.PanoramaID)
		if err != nil {
			return err
		}
		err = q.ReplaceRoundPanorama(ctx, store.ReplaceRoundPanoramaParams{
			GameID: id, N: int32(n), PanoramaID: c.ID, Provider: c.Provider,
			NearLat: near.Lat, NearLon: near.Lon, SnapKm: g.Map.Pick.SnapKm, Heading: float32(heading),
			CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		p, _ := s.Reg.Get(c.Provider)
		rd.View = p.View(c.Pano, near, g.Map.Pick.SnapKm, heading)
		out = rd
		counted = c.Provider
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Usage.Count(ctx, counted)
	return out, nil
}

// Hint spends one hint: the district (on a city map) or the region, and a circle that holds
// the real place without being centred on it. Each next hint narrows it down.
func (s *Service) Hint(ctx context.Context, userID, id uuid.UUID, n int) (*Hint, error) {
	var out *Hint
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		rd, err := g.current(n)
		if err != nil {
			return err
		}
		if rd.HintsLeft <= 0 {
			return ErrNoHints
		}
		rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: id, UserID: userID})
		if err != nil {
			return err
		}
		row := rows[len(rows)-1]
		actual := geo.Point{Lat: row.ActualLat, Lon: row.ActualLon}
		seed, err := s.seed(ctx, q, id)
		if err != nil {
			return err
		}
		h, err := hintFor(ctx, q, g.Rules.Hints.Kind, g.Map.SizeKm, len(rd.Hints), actual, seed, n)
		if err != nil {
			return err
		}
		hints := append(rd.Hints, h)
		hintsJSON, _ := json.Marshal(hints)
		if err := q.UpsertHints(ctx, store.UpsertHintsParams{GameID: id, N: int32(n), UserID: userID, Hints: hintsJSON}); err != nil {
			return err
		}
		if err := q.AddHintToGame(ctx, id); err != nil {
			return err
		}
		out = &h
		return nil
	})
	return out, err
}

// HintFor is hint i of round n of a game on a map of size mapKm: the same for every player
// of a room.
func (s *Service) HintFor(ctx context.Context, kind rules.HintKind, mapKm float64, i int, actual geo.Point, seed int64, n int) (Hint, error) {
	return hintFor(ctx, store.New(s.Pool), kind, mapKm, i, actual, seed, n)
}

func hintFor(ctx context.Context, q *store.Queries, kind rules.HintKind, mapKm float64, i int, actual geo.Point, seed int64, n int) (Hint, error) {
	r := rng(seed, n, 1000+i)
	if kind == rules.HintAuto {
		info, err := places.Lookup(ctx, q, actual)
		if err != nil {
			return Hint{}, err
		}
		return makeHint(mapKm, i, info, actual, r), nil
	}
	at, err := q.PlacesAt(ctx, store.PlacesAtParams{Lat: actual.Lat, Lon: actual.Lon})
	if err != nil {
		return Hint{}, err
	}
	region, district := hintLevels(at)
	return nextHint(kind, mapKm, i, region, district, actual, r), nil
}

// hintLevel is a place a hint can name.
type hintLevel struct {
	kind   string // region | district
	slug   string
	name   places.Text
	sizeKm float64
}

// hintLevels are the region and the district (or town) holding a point, if any.
func hintLevels(at []store.PlacesAtRow) (region, district *hintLevel) {
	for _, pl := range at { // smallest first
		lv := &hintLevel{slug: pl.Slug, name: places.NewText(pl.NameUz, pl.NameRu, pl.NameEn), sizeKm: pl.SizeKm}
		switch {
		case pl.Kind == "region" || pl.Kind == "city" && pl.ParentKind == "country":
			if region == nil {
				lv.kind = "region"
				region = lv
			}
		case (pl.Kind == "district" || pl.Kind == "city") && region == nil && district == nil:
			lv.kind = "district"
			district = lv
		}
	}
	return region, district
}

// nextHint is hint i of a round. Hints start where the rule says and narrow down: the
// region, then the district, then circles, each half the last. A name that would not
// narrow anything down on this map (the region on a Tashkent map, the district on its own
// map) is skipped.
func nextHint(kind rules.HintKind, mapKm float64, i int, region, district *hintLevel, actual geo.Point, r *rand.Rand) Hint {
	var ladder []*hintLevel
	useful := func(l *hintLevel) bool { return l != nil && l.sizeKm < mapKm*0.8 }
	if kind == rules.HintRegion && useful(region) {
		ladder = append(ladder, region)
	}
	if (kind == rules.HintRegion || kind == rules.HintDistrict) && useful(district) {
		ladder = append(ladder, district)
	}
	if i < len(ladder) {
		l := ladder[i]
		return Hint{Kind: l.kind, Text: l.name, Place: l.slug}
	}
	// v0's circle: 4% of the map's size, at least 1 km; inside a named place, a quarter of it
	base := math.Max(mapKm*0.04, 1)
	if len(ladder) > 0 {
		base = math.Min(base, math.Max(ladder[len(ladder)-1].sizeKm*0.25, 0.5))
	}
	h := Hint{Kind: "circle", RadiusKm: base / math.Pow(2, float64(i-len(ladder)))}
	h.Center = geo.Offset(actual, h.RadiusKm*0.6*r.Float64(), r.Float64()*2*math.Pi)
	return h
}

// makeHint is the hint of games from before PLAN 4.4: a name and a circle at once.
func makeHint(sizeKm float64, i int, info places.Info, actual geo.Point, r *rand.Rand) Hint {
	h := Hint{Kind: "region"}
	switch {
	case (sizeKm < 100 || i > 0) && info.District != nil:
		h.Kind, h.Text = "district", *info.District
	case (sizeKm < 100 || i > 0) && info.City != nil:
		h.Kind, h.Text = "district", *info.City
	case info.Region != nil:
		h.Text = *info.Region
	case info.City != nil:
		h.Text = *info.City
	}
	// v0: a circle of 4% of the map's size (at least 1 km); each next hint halves it
	h.RadiusKm = math.Max(sizeKm*0.04, 1) / math.Pow(2, float64(i))
	h.Center = geo.Offset(actual, h.RadiusKm*0.6*r.Float64(), r.Float64()*2*math.Pi)
	return h
}

// Guess ends the round. pos nil = no guess (time ran out without a pin). A guess after the
// deadline scores 0. A repeated guess returns the first result.
func (s *Service) Guess(ctx context.Context, userID, id uuid.UUID, n int, pos *geo.Point) (*Result, error) {
	if pos != nil && !pos.Valid() {
		return nil, ErrBadInput
	}
	var out *Result
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		if n >= 1 && n <= len(g.Rounds) && g.Rounds[n-1].Result != nil {
			out = g.Rounds[n-1].Result // already guessed: same answer again
			out.Total = g.Total
			return nil
		}
		rd, err := g.current(n)
		if err != nil {
			return err
		}
		rows, err := q.GameRounds(ctx, store.GameRoundsParams{GameID: id, UserID: userID})
		if err != nil {
			return err
		}
		row := rows[len(rows)-1]
		actual := geo.Point{Lat: row.ActualLat, Lon: row.ActualLon}

		now := s.now()
		deadline := row.Deadline
		if deadline == nil && row.ShownAt == nil && g.Rules.TimeSec > 0 {
			d := row.CreatedAt.Add(showWindow + time.Duration(g.Rules.TimeSec)*time.Second)
			deadline = &d
		}
		late := deadline != nil && now.After(deadline.Add(guessGrace))

		res := &Result{Actual: actual, Guess: pos, HintsUsed: len(rd.Hints), Late: late}
		params := store.SaveGuessParams{GameID: id, N: int32(n), UserID: userID, Late: late}
		if pos != nil {
			km := geo.DistanceKm(actual, *pos)
			res.Km = &km
			if !late {
				res.Score, res.Base = scored(km, g.Map.SizeKm, g.Rules, len(rd.Hints))
			}
			params.Lat, params.Lon, params.Km = &pos.Lat, &pos.Lon, &km
		}
		params.Score = int32(res.Score)
		if err := q.SaveGuess(ctx, params); err != nil {
			return err
		}
		if err := q.AddToGame(ctx, store.AddToGameParams{ID: id, Score: int32(res.Score), Hints: 0}); err != nil {
			return err
		}
		res.Total = g.Total + res.Score
		if g.Rules.Last(n) {
			res.GameOver = true
			if err := q.FinishGame(ctx, id); err != nil {
				return err
			}
		}
		if res.Place, err = places.Lookup(ctx, q, actual); err != nil {
			return err
		}
		out = res
		return nil
	})
	if err == nil {
		metrics.RoundsScored.Inc()
	}
	return out, err
}

// Finish ends a game when the player says so: an endless one, after any round. A game
// without a single guessed round is dropped (abandoned), not kept as a 0.
func (s *Service) Finish(ctx context.Context, userID, id uuid.UUID) (*Game, error) {
	err := s.inGame(ctx, userID, id, func(q *store.Queries, g *Game) error {
		if g.Status != "playing" {
			return nil
		}
		played := 0
		for _, r := range g.Rounds {
			if r.Result != nil {
				played++
			} else if r.ShownAt != nil {
				return ErrWrongPhase // a round is on: guess first (or let the time run out)
			}
		}
		if played == 0 {
			return q.AbandonGame(ctx, id)
		}
		return q.FinishGame(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id)
}

// History is a page of a player's finished games, newest first.
type ListItem struct {
	ID         uuid.UUID
	MapSlug    string
	MapName    places.Text
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Total      int
	Rounds     int
	Players    int // a room game: how many played it
	Hints      int // hints taken, when they cost points
}

func (s *Service) History(ctx context.Context, userID uuid.UUID, before *time.Time, offset, limit int) ([]ListItem, error) {
	rows, err := store.New(s.Pool).ListUserGames(ctx, store.ListUserGamesParams{UserID: userID, Before: before, Off: int32(offset), Lim: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListItem{
			ID: r.ID, MapSlug: r.MapSlug, MapName: places.NewText(r.NameUz, r.NameRu, r.NameEn),
			Status: r.Status, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Total: int(r.Total), Rounds: int(r.Played),
			Players: int(r.Players), Hints: CostlyHints(r.Rules, int(r.HintsUsed)),
		})
	}
	return out, nil
}
