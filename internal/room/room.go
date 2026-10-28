// Package room runs live rooms: friends on one map, the same panorama at the same time,
// picked and scored by the server with the same rules as solo games (PLAN 5).
//
// A room lives in the API's memory. Every change is saved to Valkey before anyone hears of
// it, so a deploy or a crash only pauses a room: the next process loads it on the first
// request for it, the round's clock goes on from its deadline, and the players' pages
// reconnect by themselves. Every player's finished rounds go to Postgres as their own game
// (mode 'room'), so history, profiles and progress per place count room games too.
package room

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/score"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Errors carry the stable codes the API returns. The game's own (wrong_phase, no_hints,
// too_far, …) pass through as they are.
var (
	ErrNotFound     = errors.New("no_room")
	ErrFull         = errors.New("room_full")
	ErrKicked       = errors.New("kicked")
	ErrNotHost      = errors.New("not_host")
	ErrNotInRoom    = errors.New("not_in_room")
	ErrTooManyRooms = errors.New("too_many_rooms")
)

const (
	// MaxPlayers in a room (v0's 12).
	MaxPlayers = 12
	// After the first guess the others get this long, so nobody waits forever (v0).
	afterFirstGuess = 45 * time.Second
	// A guess sent right at the deadline still counts.
	guessGrace = 2 * time.Second
	// The clock starts when the first viewer opens the panorama, at the latest this long
	// after the round began.
	showWindow = 60 * time.Second
	// A player whose page is gone (a reload, a phone in a pocket, a deploy) is waited for
	// this long before a round ends without them; the host keeps the room this long.
	awayGrace = 20 * time.Second
	// A room nobody is connected to closes after this.
	idleTTL = 15 * time.Minute
	// A round's result stays this long for the reveal (the line, the flag, the score), then a
	// ring counts autoNext on every phone and the next round starts by itself.
	revealFor = 3 * time.Second
	autoNext  = 10 * time.Second
	// How many times a round may swap a panorama no viewer could open.
	maxReplace = 5
	// A round that cannot be written to Postgres is tried again this often, this many times.
	persistEvery = 5 * time.Second
	persistTries = 20
)

type Phase string

const (
	Lobby   Phase = "lobby"   // waiting for the host to start
	Playing Phase = "playing" // a round is on
	Result  Phase = "result"  // a round's result; the next round starts by itself, or the host goes on
	Final   Phase = "final"   // the game's table; the host starts again or changes the rules
)

// Who is the player behind a request: the session's user.
type Who struct {
	ID     uuid.UUID
	Name   string
	Avatar string
	Guest  bool
}

// Conn is one live connection of a player's page (a WebSocket).
type Conn interface {
	Send(msg []byte)     // never blocks: a connection that cannot keep up closes itself
	Close(reason string) // kicked, left, room closed, or the server restarting
}

// state is everything a room is, as saved to Valkey.
type state struct {
	Code       string                  `json:"code"`
	Ver        int64                   `json:"ver"`
	CreatedAt  time.Time               `json:"createdAt"`
	Map        string                  `json:"map"`
	Rules      rules.Rules             `json:"rules"`
	Host       uuid.UUID               `json:"host"`
	Players    []*Player               `json:"players"`
	Kicked     []uuid.UUID             `json:"kicked,omitempty"`
	Phase      Phase                   `json:"phase"`
	Games      int                     `json:"games"`  // games started in this room
	GameID     uuid.UUID               `json:"gameId"` // games.room_game of the current game
	StartedAt  time.Time               `json:"startedAt"`
	Seed       int64                   `json:"seed"`
	SizeKm     float64                 `json:"sizeKm"`
	GameRows   map[uuid.UUID]uuid.UUID `json:"gameRows"` // each player's games row of this game
	Closed     bool                    `json:"closed"`   // the game's rows are finished
	Round      *Round                  `json:"round,omitempty"`
	Done       []*Done                 `json:"done"`
	NextAt     *time.Time              `json:"nextAt,omitempty"` // a result: when the room goes on by itself; nil once the host holds it
	EmptySince *time.Time              `json:"emptySince,omitempty"`
}

type Player struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Avatar    string     `json:"avatar"`
	Guest     bool       `json:"guest"`
	Total     int        `json:"total"`
	HintsUsed int        `json:"hintsUsed"` // in this game, for per-game hint rules
	Left      bool       `json:"left,omitempty"`
	AwaySince *time.Time `json:"awaySince,omitempty"` // no page connected since then
	conns     int
}

type Round struct {
	N         int                       `json:"n"`
	PanoID    int64                     `json:"panoId"`
	Provider  string                    `json:"provider"`
	Ref       string                    `json:"ref"`
	Library   geo.Point                 `json:"library"` // the panorama's position in the library
	Near      geo.Point                 `json:"near"`
	SnapKm    float64                   `json:"snapKm"`
	Heading   float64                   `json:"heading"`
	Swap      int                       `json:"swap"` // panoramas swapped because no viewer could open one
	CreatedAt time.Time                 `json:"createdAt"`
	ShownAt   *time.Time                `json:"shownAt,omitempty"`
	Deadline  *time.Time                `json:"deadline,omitempty"`
	Actual    geo.Point                 `json:"actual"` // where it is scored: what the first viewer opened
	In        map[uuid.UUID]bool        `json:"in"`     // players of this round
	Seen      map[uuid.UUID]bool        `json:"seen"`   // in it with a page open: their result is saved
	Hints     map[uuid.UUID][]game.Hint `json:"hints"`
	Pins      map[uuid.UUID]geo.Point   `json:"pins"`
	Guesses   map[uuid.UUID]*Guess      `json:"guesses"`
}

type Guess struct {
	Pos *geo.Point `json:"pos,omitempty"`
	At  time.Time  `json:"at"`
}

// Done is a finished round of the current game.
type Done struct {
	Round  *Round      `json:"round"`
	Place  places.Info `json:"place"`
	Scores []Score     `json:"scores"`
	Saved  bool        `json:"saved"`
	Tries  int         `json:"tries,omitempty"`
}

type Score struct {
	Player uuid.UUID  `json:"player"`
	Guess  *geo.Point `json:"guess,omitempty"`
	Km     *float64   `json:"km,omitempty"`
	Score  int        `json:"score"`
	Base   int        `json:"base,omitempty"` // the score before hints, when they took points
	Hints  int        `json:"hints"`
}

// Room is one live room. Every method locks it.
type Room struct {
	hub   *Hub
	mu    sync.Mutex
	st    state
	m     *maps.Map
	conns map[Conn]uuid.UUID
	busy  atomic.Bool // a tick is running
	gone  bool        // closed and out of the hub

	lastSave    time.Time
	lastPersist time.Time
}

func (r *Room) now() time.Time { return r.hub.now() }

func (r *Room) player(id uuid.UUID) *Player {
	for _, p := range r.st.Players {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// member is the player behind a request, who must be in the room.
func (r *Room) member(id uuid.UUID) (*Player, error) {
	if r.gone {
		return nil, ErrNotFound
	}
	p := r.player(id)
	if p == nil || p.Left {
		return nil, ErrNotInRoom
	}
	return p, nil
}

func (r *Room) host(id uuid.UUID) (*Player, error) {
	p, err := r.member(id)
	if err != nil {
		return nil, err
	}
	if r.st.Host != id {
		return nil, ErrNotHost
	}
	return p, nil
}

// present: the player has a page open, or had one a moment ago.
func (r *Room) present(p *Player, now time.Time) bool {
	return p.conns > 0 || p.AwaySince == nil || now.Sub(*p.AwaySince) < awayGrace
}

func (r *Room) active() []*Player {
	var out []*Player
	for _, p := range r.st.Players {
		if !p.Left {
			out = append(out, p)
		}
	}
	return out
}

// changed saves the room and tells every page. Called with the lock held, after every change.
func (r *Room) changed(ctx context.Context) {
	r.st.Ver++
	r.save(ctx)
	r.broadcast()
}

func (r *Room) save(ctx context.Context) {
	b, err := json.Marshal(&r.st)
	if err != nil {
		slog.ErrorContext(ctx, "room snapshot", "room", r.st.Code, "err", err)
		return
	}
	if err := r.hub.Store.Save(ctx, r.st.Code, b); err != nil {
		slog.WarnContext(ctx, "room snapshot not saved", "room", r.st.Code, "err", err)
		return
	}
	r.lastSave = r.now()
}

func (r *Room) broadcast() {
	if r.hub.Encode == nil {
		return
	}
	cache := map[uuid.UUID][]byte{}
	for c, id := range r.conns {
		msg, ok := cache[id]
		if !ok {
			msg = r.hub.Encode(r.view(id))
			cache[id] = msg
		}
		c.Send(msg)
	}
}

// ---------- joining and leaving ----------

// Check says whether a player may open the room: not kicked, and a seat for a newcomer.
func (r *Room) Check(id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone {
		return ErrNotFound
	}
	if slices.Contains(r.st.Kicked, id) {
		return ErrKicked
	}
	if p := r.player(id); (p == nil || p.Left) && len(r.active()) >= MaxPlayers {
		return ErrFull
	}
	return nil
}

// Join connects a player's page, adding the player to the room if they are new. A player
// who left comes back; a kicked one cannot.
func (r *Room) Join(ctx context.Context, c Conn, who Who) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone {
		return ErrNotFound
	}
	if slices.Contains(r.st.Kicked, who.ID) {
		return ErrKicked
	}
	p := r.player(who.ID)
	if p == nil || p.Left {
		if len(r.active()) >= MaxPlayers {
			return ErrFull
		}
	}
	if p == nil {
		p = &Player{ID: who.ID}
		r.st.Players = append(r.st.Players, p)
	}
	p.Name, p.Avatar, p.Guest, p.Left = who.Name, who.Avatar, who.Guest, false
	p.conns++
	p.AwaySince = nil
	r.conns[c] = who.ID
	r.st.EmptySince = nil
	if r.player(r.st.Host) == nil || r.player(r.st.Host).Left {
		r.st.Host = who.ID
	}
	if rd := r.st.Round; rd != nil && r.st.Phase == Playing {
		rd.In[who.ID] = true
		rd.Seen[who.ID] = true
	}
	r.changed(ctx)
	return nil
}

// Drop is a page that went away. The player keeps the seat, to come back.
func (r *Room) Drop(ctx context.Context, c Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.conns[c]
	if !ok {
		return
	}
	delete(r.conns, c)
	if p := r.player(id); p != nil {
		p.conns--
		if p.conns <= 0 {
			p.conns = 0
			now := r.now()
			p.AwaySince = &now
		}
	}
	if len(r.conns) == 0 {
		now := r.now()
		r.st.EmptySince = &now
	}
	if !r.gone {
		r.changed(ctx)
	}
}

// Quit: the player leaves for good. Between games the seat goes; during one the scores stay
// in the table.
func (r *Room) Quit(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.member(id)
	if err != nil {
		return err
	}
	if r.st.Phase == Lobby || r.st.Phase == Final {
		r.remove(id)
	} else {
		p.Left = true
	}
	r.closeConns(id, "left")
	if r.st.Host == id {
		r.passHost(true)
	}
	r.endIfAllGuessed(ctx) // the round was waiting for them
	r.changed(ctx)
	return nil
}

// Kick: the host sends a player away; they cannot come back to this room.
func (r *Room) Kick(ctx context.Context, id, target uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if target == id || r.player(target) == nil {
		return game.ErrBadInput
	}
	r.remove(target)
	r.st.Kicked = append(r.st.Kicked, target)
	if rd := r.st.Round; rd != nil {
		delete(rd.In, target)
	}
	r.closeConns(target, "kicked")
	r.endIfAllGuessed(ctx)
	r.changed(ctx)
	return nil
}

// MakeHost: the host hands the room to another player.
func (r *Room) MakeHost(ctx context.Context, id, target uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if _, err := r.member(target); err != nil {
		return game.ErrBadInput
	}
	r.st.Host = target
	r.changed(ctx)
	return nil
}

func (r *Room) remove(id uuid.UUID) {
	r.st.Players = slices.DeleteFunc(r.st.Players, func(p *Player) bool { return p.ID == id })
}

func (r *Room) closeConns(id uuid.UUID, reason string) {
	for c, who := range r.conns {
		if who == id {
			delete(r.conns, c)
			c.Close(reason)
		}
	}
}

// passHost gives the room to the first player with a page open (anyone, if force and
// nobody is online).
func (r *Room) passHost(force bool) bool {
	var fallback *Player
	for _, p := range r.active() {
		if p.ID == r.st.Host {
			continue
		}
		if p.conns > 0 {
			r.st.Host = p.ID
			return true
		}
		if fallback == nil {
			fallback = p
		}
	}
	if force && fallback != nil {
		r.st.Host = fallback.ID
		return true
	}
	return false
}

// ---------- the host's game ----------

// SetRules: the host changes the map or the rules between games.
func (r *Room) SetRules(ctx context.Context, id uuid.UUID, slug string, rs *rules.Rules) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if r.st.Phase != Lobby && r.st.Phase != Final {
		return game.ErrWrongPhase
	}
	m, checked, err := r.hub.Games.Prepare(ctx, slug, rs)
	if err != nil {
		return err
	}
	r.m, r.st.Map, r.st.Rules = m, m.Slug, checked
	r.st.Phase = Lobby
	r.changed(ctx)
	return nil
}

// Start: the host starts a game, from the lobby or again after one (rematch).
func (r *Room) Start(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if r.st.Phase != Lobby && r.st.Phase != Final {
		return game.ErrWrongPhase
	}
	if r.st.Games > 0 {
		r.persist(ctx) // the last game's rows, before they are forgotten
	}
	m, rs, err := r.hub.Games.Prepare(ctx, r.st.Map, &r.st.Rules)
	if err != nil {
		return err
	}
	seed := rand.Int64()
	pick, err := r.hub.Games.PickRound(ctx, m, rs, maps.Used{IDs: map[int64]bool{}}, seed, 1, 0)
	if err != nil {
		return err
	}
	now := r.now()
	r.m, r.st.Rules = m, rs
	r.st.Games++
	r.st.GameID = uuid.New()
	r.st.StartedAt = now
	r.st.Seed = seed
	r.st.SizeKm = m.SizeKm
	r.st.GameRows = map[uuid.UUID]uuid.UUID{}
	r.st.Closed = false
	r.st.Done = nil
	r.st.Players = slices.DeleteFunc(r.st.Players, func(p *Player) bool { return p.Left })
	for _, p := range r.st.Players {
		p.Total, p.HintsUsed = 0, 0
	}
	r.beginRound(ctx, 1, pick)
	r.changed(ctx)
	return nil
}

// Next: the host goes on from a round's result, to the next round or (finish, or after the
// last round) to the game's table.
func (r *Room) Next(ctx context.Context, id uuid.UUID, finish bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if r.st.Phase != Result {
		return game.ErrWrongPhase
	}
	if err := r.advance(ctx, finish); err != nil {
		return err
	}
	r.changed(ctx)
	return nil
}

// Hold: the host stops the ring; the room waits for their Next.
func (r *Room) Hold(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.host(id); err != nil {
		return err
	}
	if r.st.Phase != Result {
		return game.ErrWrongPhase
	}
	if r.st.NextAt == nil {
		return nil
	}
	r.st.NextAt = nil
	r.changed(ctx)
	return nil
}

// advance goes on from a round's result: to the next round, or (finish, or after the last
// round) to the game's table.
func (r *Room) advance(ctx context.Context, finish bool) error {
	n := len(r.st.Done)
	if finish || r.st.Rules.Last(n) {
		r.finish(ctx)
		return nil
	}
	pick, err := r.hub.Games.PickRound(ctx, r.m, r.st.Rules, r.used(), r.st.Seed, n+1, 0)
	if errors.Is(err, game.ErrNoPanorama) && r.st.Rules.Endless() {
		r.finish(ctx) // an endless game that has shown every panorama of its map is done
		return nil
	}
	if err != nil {
		return err
	}
	r.beginRound(ctx, n+1, pick)
	return nil
}

func (r *Room) finish(ctx context.Context) {
	r.st.Phase = Final
	r.st.Round = nil
	r.st.NextAt = nil
	r.persist(ctx)
}

func (r *Room) used() maps.Used {
	used := maps.Used{IDs: map[int64]bool{}}
	for _, d := range r.st.Done {
		used.IDs[d.Round.PanoID] = true
		used.Pos = append(used.Pos, d.Round.Actual)
	}
	return used
}

func (r *Room) beginRound(ctx context.Context, n int, pick game.Pick) {
	rd := &Round{
		N: n, PanoID: pick.Cand.ID, Provider: pick.Cand.Provider, Ref: pick.Cand.Ref,
		Library: pick.Cand.Pos, Near: pick.Near, SnapKm: r.m.Pick.SnapKm, Heading: pick.Heading,
		CreatedAt: r.now(), Actual: pick.Cand.Pos,
		In: map[uuid.UUID]bool{}, Seen: map[uuid.UUID]bool{}, Hints: map[uuid.UUID][]game.Hint{},
		Pins: map[uuid.UUID]geo.Point{}, Guesses: map[uuid.UUID]*Guess{},
	}
	for _, p := range r.active() {
		rd.In[p.ID] = true
		if p.conns > 0 {
			rd.Seen[p.ID] = true
		}
	}
	r.st.Round = rd
	r.st.Phase = Playing
	r.st.NextAt = nil
	r.hub.Games.Usage.Count(ctx, rd.Provider)
}

// ---------- a round ----------

// current is round n, while it is being played.
func (r *Room) current(n int) (*Round, error) {
	rd := r.st.Round
	if r.st.Phase != Playing || rd == nil || rd.N != n {
		return nil, game.ErrWrongPhase
	}
	return rd, nil
}

// Shown: a viewer opened the round's panorama. The first one starts the clock and, for
// providers that search near a point, says where the round is scored.
func (r *Room) Shown(ctx context.Context, id uuid.UUID, n int, pos *geo.Point) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.member(id); err != nil {
		return err
	}
	rd, err := r.current(n)
	if err != nil {
		return err
	}
	rd.Seen[id] = true
	if rd.ShownAt != nil {
		return nil
	}
	actual, err := r.hub.Games.SnapCheck(rd.Provider, rd.Near, rd.Library, rd.SnapKm, pos)
	if err != nil {
		return err
	}
	r.startClock(r.now(), actual)
	r.changed(ctx)
	return nil
}

func (r *Room) startClock(at time.Time, actual geo.Point) {
	rd := r.st.Round
	rd.ShownAt, rd.Actual = &at, actual
	if r.st.Rules.TimeSec > 0 {
		d := at.Add(time.Duration(r.st.Rules.TimeSec) * time.Second)
		rd.Deadline = &d
	}
}

// Fail: a viewer could not open the panorama. Before anyone has, the round gets another one
// for everybody; swap is the panorama the page tried, so two pages failing at once swap once.
func (r *Room) Fail(ctx context.Context, id uuid.UUID, n, swap int, notFound bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.member(id); err != nil {
		return err
	}
	rd, err := r.current(n)
	if err != nil {
		return err
	}
	if rd.ShownAt != nil {
		return game.ErrWrongPhase // others see it: this page guesses on the map alone
	}
	if swap != rd.Swap {
		return nil // already swapped
	}
	if rd.Swap >= maxReplace {
		return game.ErrTooManyFails
	}
	q := store.New(r.hub.Pool)
	if notFound {
		if err := q.PanoramaFailed(ctx, rd.PanoID); err != nil {
			return err
		}
	}
	used := r.used()
	used.IDs[rd.PanoID] = true
	pick, err := r.hub.Games.PickRound(ctx, r.m, r.st.Rules, used, r.st.Seed, n, rd.Swap+1)
	if err != nil {
		return err
	}
	rd.PanoID, rd.Provider, rd.Ref = pick.Cand.ID, pick.Cand.Provider, pick.Cand.Ref
	rd.Library, rd.Actual, rd.Near, rd.Heading = pick.Cand.Pos, pick.Cand.Pos, pick.Near, pick.Heading
	rd.Swap++
	rd.CreatedAt = r.now()
	r.hub.Games.Usage.Count(ctx, rd.Provider)
	r.changed(ctx)
	return nil
}

// Hint spends one of the player's hints. Every player gets the same hints for a round.
func (r *Room) Hint(ctx context.Context, id uuid.UUID, n int) (*game.Hint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.member(id)
	if err != nil {
		return nil, err
	}
	rd, err := r.current(n)
	if err != nil {
		return nil, err
	}
	if rd.Guesses[id] != nil {
		return nil, game.ErrWrongPhase
	}
	mine := rd.Hints[id]
	if r.st.Rules.HintsLeft(len(mine), p.HintsUsed) <= 0 {
		return nil, game.ErrNoHints
	}
	h, err := r.hub.Games.HintFor(ctx, r.st.Rules.Hints.Kind, r.st.SizeKm, len(mine), rd.Actual, r.st.Seed, n)
	if err != nil {
		return nil, err
	}
	rd.Hints[id] = append(mine, h)
	rd.Seen[id] = true
	p.HintsUsed++
	r.changed(ctx)
	return &h, nil
}

// Pin: the player moved their pin without pressing Guess yet. If the round ends first (the
// time ran out, the phone locked), the pin counts. Others do not hear of it.
func (r *Room) Pin(ctx context.Context, id uuid.UUID, n int, pos *geo.Point) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.member(id); err != nil {
		return err
	}
	rd, err := r.current(n)
	if err != nil {
		return err
	}
	if rd.Guesses[id] != nil {
		return nil
	}
	if pos != nil && !pos.Valid() {
		return game.ErrBadInput
	}
	if pos == nil {
		delete(rd.Pins, id)
	} else {
		rd.Pins[id] = *pos
	}
	rd.Seen[id] = true
	r.st.Ver++
	r.save(ctx)
	return nil
}

// Guess: the player's answer (pos nil = none). The round ends when everyone present has
// answered, or at its deadline; after the first answer the others get afterFirstGuess.
func (r *Room) Guess(ctx context.Context, id uuid.UUID, n int, pos *geo.Point) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.member(id); err != nil {
		return err
	}
	if pos != nil && !pos.Valid() {
		return game.ErrBadInput
	}
	if r.st.Phase != Playing && len(r.st.Done) >= n {
		return nil // the round is over: a guess sent twice, or late
	}
	rd, err := r.current(n)
	if err != nil {
		return err
	}
	if rd.Guesses[id] != nil {
		return nil
	}
	now := r.now()
	if rd.Deadline != nil && now.After(rd.Deadline.Add(guessGrace)) {
		return game.ErrWrongPhase // the round ends on the next tick, with the player's pin
	}
	first := len(rd.Guesses) == 0
	rd.Guesses[id] = &Guess{Pos: pos, At: now}
	rd.In[id], rd.Seen[id] = true, true
	if first && rd.ShownAt != nil {
		if limit := now.Add(afterFirstGuess); rd.Deadline == nil || limit.Before(*rd.Deadline) {
			rd.Deadline = &limit
		}
	}
	r.endIfAllGuessed(ctx)
	r.changed(ctx)
	return nil
}

// endIfAllGuessed ends the round when every player of it who is present has answered.
func (r *Room) endIfAllGuessed(ctx context.Context) {
	rd := r.st.Round
	if r.st.Phase != Playing || rd == nil {
		return
	}
	now := r.now()
	waiting, answered := 0, 0
	for _, p := range r.active() {
		if !rd.In[p.ID] {
			continue
		}
		if rd.Guesses[p.ID] != nil {
			answered++
		} else if r.present(p, now) {
			waiting++
		}
	}
	if answered > 0 && waiting == 0 {
		r.endRound(ctx)
	}
}

// endRound scores every player of the round: their guess, else their last pin, else 0.
func (r *Room) endRound(ctx context.Context) {
	rd := r.st.Round
	place, err := places.Lookup(ctx, store.New(r.hub.Pool), rd.Actual)
	if err != nil {
		slog.WarnContext(ctx, "room round place", "room", r.st.Code, "err", err)
	}
	d := &Done{Round: rd, Place: place}
	for _, p := range r.st.Players {
		if !rd.In[p.ID] {
			continue
		}
		var pos *geo.Point
		if g := rd.Guesses[p.ID]; g != nil {
			pos = g.Pos
		} else if pin, ok := rd.Pins[p.ID]; ok {
			pos = &pin
		}
		sc := Score{Player: p.ID, Guess: pos, Hints: len(rd.Hints[p.ID])}
		if pos != nil {
			km := geo.DistanceKm(rd.Actual, *pos)
			sc.Km = &km
			base := score.Round(km, r.st.SizeKm, r.st.Rules.ScoreScale)
			sc.Score = score.WithHints(base, sc.Hints, r.st.Rules.Hints.Cost)
			if sc.Score != base {
				sc.Base = base
			}
		}
		p.Total += sc.Score
		d.Scores = append(d.Scores, sc)
	}
	rd.Pins = nil
	r.st.Done = append(r.st.Done, d)
	r.st.Round = nil
	r.st.Phase = Result
	next := r.now().Add(revealFor + autoNext)
	r.st.NextAt = &next
	r.persist(ctx)
}

// ---------- Postgres ----------

// persist writes the finished rounds not written yet, each player's into their own game
// (made with their first round), and finishes the rows of a game that is over. A failure
// is tried again by the ticks.
func (r *Room) persist(ctx context.Context) {
	r.lastPersist = r.now()
	for _, d := range r.st.Done {
		if d.Saved {
			continue
		}
		if err := r.writeRound(ctx, d); err != nil {
			d.Tries++
			if d.Tries >= persistTries {
				slog.ErrorContext(ctx, "room round lost", "room", r.st.Code, "round", d.Round.N, "err", err)
				d.Saved = true
				continue
			}
			slog.WarnContext(ctx, "room round not saved yet", "room", r.st.Code, "round", d.Round.N, "err", err)
			return // in order: the next rounds wait for this one
		}
		d.Saved = true
	}
	if r.st.Phase == Final && !r.st.Closed {
		ids := make([]uuid.UUID, 0, len(r.st.GameRows))
		for _, id := range r.st.GameRows {
			ids = append(ids, id)
		}
		if err := store.New(r.hub.Pool).FinishRoomGames(ctx, ids); err != nil {
			slog.WarnContext(ctx, "room games not finished yet", "room", r.st.Code, "err", err)
			return
		}
		r.st.Closed = true
	}
}

func (r *Room) pending() bool {
	for _, d := range r.st.Done {
		if !d.Saved {
			return true
		}
	}
	return r.st.Phase == Final && !r.st.Closed
}

func (r *Room) writeRound(ctx context.Context, d *Done) error {
	tx, err := r.hub.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // after Commit it is a no-op
	q := store.New(tx)
	rd := d.Round
	made := map[uuid.UUID]uuid.UUID{}
	rulesJSON, _ := json.Marshal(r.st.Rules)
	code := r.st.Code
	for _, sc := range d.Scores {
		if !rd.Seen[sc.Player] {
			continue // never had the room open during this round
		}
		gameID, ok := r.st.GameRows[sc.Player]
		if !ok {
			gameID, err = q.CreateRoomGame(ctx, store.CreateRoomGameParams{
				UserID: sc.Player, MapID: r.m.ID, Rules: rulesJSON, SizeKm: r.st.SizeKm, Seed: r.st.Seed,
				RoomCode: &code, RoomGame: &r.st.GameID, StartedAt: r.st.StartedAt,
			})
			if err != nil {
				return err
			}
			made[sc.Player] = gameID
		}
		err = q.CreateRoomRound(ctx, store.CreateRoomRoundParams{
			GameID: gameID, N: int32(rd.N), PanoramaID: rd.PanoID, Provider: rd.Provider,
			NearLat: rd.Near.Lat, NearLon: rd.Near.Lon, SnapKm: rd.SnapKm, Heading: float32(rd.Heading),
			ActualLat: rd.Actual.Lat, ActualLon: rd.Actual.Lon,
			CreatedAt: rd.CreatedAt, ShownAt: rd.ShownAt, Deadline: rd.Deadline, Replaced: int32(rd.Swap),
		})
		if err != nil {
			return err
		}
		hints, _ := json.Marshal(orEmpty(rd.Hints[sc.Player]))
		params := store.SaveRoomGuessParams{GameID: gameID, N: int32(rd.N), UserID: sc.Player, Hints: hints, Score: int32(sc.Score), Km: sc.Km}
		if sc.Guess != nil {
			params.Lat, params.Lon = &sc.Guess.Lat, &sc.Guess.Lon
		}
		if g := rd.Guesses[sc.Player]; g != nil {
			params.GuessedAt = &g.At
		} else {
			end := r.now()
			params.GuessedAt = &end
		}
		rows, err := q.SaveRoomGuess(ctx, params)
		if err != nil {
			return err
		}
		if rows == 1 {
			if err := q.AddToGame(ctx, store.AddToGameParams{ID: gameID, Score: int32(sc.Score), Hints: int32(sc.Hints)}); err != nil {
				return err
			}
		}
	}
	if rd.ShownAt != nil {
		if err := q.PanoramaOpened(ctx, rd.PanoID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for id, g := range made {
		r.st.GameRows[id] = g
	}
	return nil
}

func orEmpty(h []game.Hint) []game.Hint {
	if h == nil {
		return []game.Hint{}
	}
	return h
}

// ---------- the clock ----------

// tick runs twice a second: it starts a clock nobody's viewer started, ends rounds at their
// deadline or when the last present player answered, goes on from a result when its ring
// runs out, hands the room on from a host who is gone, retries Postgres, and closes a room
// nobody has had open for idleTTL.
func (r *Room) tick(ctx context.Context) {
	if !r.busy.CompareAndSwap(false, true) {
		return
	}
	defer r.busy.Store(false)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone {
		return
	}
	now := r.now()
	changed := false

	if rd := r.st.Round; r.st.Phase == Playing && rd != nil {
		if rd.ShownAt == nil && now.After(rd.CreatedAt.Add(showWindow)) {
			r.startClock(rd.CreatedAt.Add(showWindow), rd.Library)
			changed = true
		}
		if rd.Deadline != nil && now.After(rd.Deadline.Add(guessGrace)) {
			r.endRound(ctx)
			changed = true
		} else {
			v := r.st.Ver
			r.endIfAllGuessed(ctx) // someone away for longer than awayGrace is not waited for
			changed = changed || r.st.Phase != Playing || v != r.st.Ver
		}
	}
	if r.st.Phase == Result && r.st.NextAt != nil && !now.Before(*r.st.NextAt) {
		// nobody there (everyone gone longer than awayGrace): the room waits for them, and
		// does not spend panoramas on an empty room
		if !slices.ContainsFunc(r.active(), func(p *Player) bool { return r.present(p, now) }) {
			r.st.NextAt = nil
		} else if err := r.advance(ctx, false); err != nil {
			slog.WarnContext(ctx, "room next round", "room", r.st.Code, "err", err)
			r.st.NextAt = nil // the host's Next tries again, and says why
		}
		changed = true
	}
	if h := r.player(r.st.Host); h == nil || h.Left || (h.conns == 0 && h.AwaySince != nil && now.Sub(*h.AwaySince) >= awayGrace) {
		if r.passHost(h == nil || h.Left) {
			changed = true
		}
	}
	if r.pending() && now.Sub(r.lastPersist) >= persistEvery {
		r.persist(ctx)
		changed = true
	}
	if r.st.EmptySince != nil && now.Sub(*r.st.EmptySince) >= idleTTL {
		r.close(ctx)
		return
	}
	if changed {
		r.changed(ctx)
	} else if len(r.conns) > 0 && now.Sub(r.lastSave) > r.hub.Store.TTL()/3 {
		r.save(ctx) // keeps a quiet lobby from expiring in Valkey
	}
}

// close ends a room nobody has open: its game's rounds are saved and its rows finished.
func (r *Room) close(ctx context.Context) {
	if r.st.Phase == Playing || r.st.Phase == Result {
		r.st.Phase = Final
		r.st.Round = nil
	}
	r.persist(ctx)
	if err := r.hub.Store.Delete(ctx, r.st.Code); err != nil {
		slog.WarnContext(ctx, "room snapshot not deleted", "room", r.st.Code, "err", err)
	}
	for c := range r.conns {
		c.Close("closed")
	}
	r.conns = map[Conn]uuid.UUID{}
	r.gone = true
	r.hub.forget(r)
}
