package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxPlayers = 12
	maxNameLen = 24
	// After the first guess the others get this long, so nobody waits forever.
	afterFirstGuess = 45 * time.Second
	// Guesses sent right at the deadline still count.
	deadlineGrace = 1500 * time.Millisecond
)

type Phase string

const (
	PhaseLobby   Phase = "lobby"
	PhasePicking Phase = "picking" // the host's browser is looking for a panorama
	PhasePlaying Phase = "playing"
	PhaseResult  Phase = "result"
	PhaseFinal   Phase = "final"
)

var (
	errNoRoom     = errors.New("no_room")
	errFull       = errors.New("full")
	errBadName    = errors.New("bad_name")
	errNotHost    = errors.New("not_host")
	errWrongPhase = errors.New("wrong_phase")
	errBadInput   = errors.New("bad_input")
)

type Settings struct {
	Area   string `json:"area"`
	Diff   string `json:"diff"`
	Rounds int    `json:"rounds"`
	Time   int    `json:"time"` // seconds per round, 0 = no limit
}

func (s Settings) valid() bool {
	_, okArea := areaScaleKm[s.Area]
	_, okDiff := difficulties[s.Diff]
	return okArea && okDiff && s.Rounds >= 1 && s.Rounds <= 10 && s.Time >= 0 && s.Time <= 600
}

// sink is where a player's messages go: a websocket client, or a fake one in tests.
type sink interface {
	queue(msg []byte)
	close()
}

type Player struct {
	ID    string
	Token string // secret that lets a reloaded page take the same seat
	Name  string
	Color int
	Total int
	out   sink // nil = offline
}

type Guess struct {
	Pos   *LatLon  `json:"pos"` // nil = no guess
	Hint  bool     `json:"hint"`
	Km    *float64 `json:"km"`
	Score int      `json:"score"`
}

type RoundRecord struct {
	Actual  LatLon            `json:"actual"`
	Guesses map[string]*Guess `json:"guesses"` // by player ID
	pins    map[string]*Guess // last pin of players who have not pressed Guess yet
	done    bool
}

type Room struct {
	mu         sync.Mutex
	Code       string
	Settings   Settings
	hostID     string
	players    []*Player
	phase      Phase
	round      int // 1-based, 0 before the first round
	rounds     []*RoundRecord
	deadline   time.Time // zero = no deadline
	timer      *time.Timer
	nextID     int
	emptySince time.Time
}

func newRoom(code string, s Settings) *Room {
	return &Room{Code: code, Settings: s, phase: PhaseLobby, emptySince: time.Now()}
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return "", errBadName
	}
	return name, nil
}

// join adds a player, or gives a returning player (same token) their seat back.
func (r *Room) join(out sink, name, token string) (*Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if token != "" {
		for _, p := range r.players {
			if p.Token == token {
				if p.out != nil && p.out != out {
					p.out.close()
				}
				p.out = out
				out.queue(welcomeMsg(r.Code, p))
				r.afterChange()
				return p, nil
			}
		}
	}

	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if len(r.players) >= maxPlayers {
		return nil, errFull
	}
	r.nextID++
	p := &Player{
		ID:    fmt.Sprintf("p%d", r.nextID),
		Token: randomToken(),
		Name:  r.uniqueName(name),
		Color: (r.nextID - 1) % maxPlayers,
		out:   out,
	}
	r.players = append(r.players, p)
	if r.hostID == "" {
		r.hostID = p.ID
	}
	out.queue(welcomeMsg(r.Code, p))
	r.afterChange()
	return p, nil
}

func (r *Room) uniqueName(name string) string {
	taken := func(n string) bool {
		for _, p := range r.players {
			if strings.EqualFold(p.Name, n) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s %d", name, i); !taken(n) {
			return n
		}
	}
}

// disconnect is called when a socket closes. The seat stays, so the player can come back.
func (r *Room) disconnect(p *Player, out sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.out != out {
		return // already replaced by a newer connection
	}
	p.out = nil
	r.afterChange()
}

// leave is an explicit exit. In the lobby the seat is removed; in a game the scores stay.
func (r *Room) leave(p *Player) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.out != nil {
		p.out.close()
		p.out = nil
	}
	if r.phase == PhaseLobby {
		for i, q := range r.players {
			if q == p {
				r.players = append(r.players[:i], r.players[i+1:]...)
				break
			}
		}
	}
	r.afterChange()
}

// setSettings: the host changes area, difficulty, rounds or time while in the lobby.
func (r *Room) setSettings(p *Player, s Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseLobby {
		return errWrongPhase
	}
	if !s.valid() {
		return errBadInput
	}
	r.Settings = s
	r.broadcast()
	return nil
}

func (r *Room) start(p *Player) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseLobby && r.phase != PhaseFinal {
		return errWrongPhase
	}
	for _, q := range r.players {
		q.Total = 0
	}
	r.rounds = nil
	r.round = 1
	r.phase = PhasePicking
	r.broadcast()
	return nil
}

// setPanorama: the host's browser found a panorama for the round.
func (r *Room) setPanorama(p *Player, round int, pos LatLon) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID != r.hostID {
		return errNotHost
	}
	if r.phase != PhasePicking || round != r.round {
		return errWrongPhase
	}
	if !pos.insideBox() {
		return errBadInput
	}
	r.rounds = append(r.rounds, &RoundRecord{Actual: pos, Guesses: map[string]*Guess{}, pins: map[string]*Guess{}})
	r.phase = PhasePlaying
	r.deadline = time.Time{}
	if r.Settings.Time > 0 {
		r.setDeadline(time.Now().Add(time.Duration(r.Settings.Time) * time.Second))
	}
	r.broadcast()
	return nil
}

func (r *Room) guess(p *Player, pos *LatLon, hint bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhasePlaying {
		return errWrongPhase
	}
	rec := r.rounds[len(r.rounds)-1]
	if _, done := rec.Guesses[p.ID]; done {
		return errWrongPhase
	}
	if pos != nil && !pos.insideBox() {
		pos = nil // a pin far outside the country scores 0 anyway
	}
	rec.Guesses[p.ID] = &Guess{Pos: pos, Hint: hint}

	if r.allOnlineGuessed() {
		r.endRound()
		return nil
	}
	if len(rec.Guesses) == 1 {
		if limit := time.Now().Add(afterFirstGuess); r.deadline.IsZero() || limit.Before(r.deadline) {
			r.setDeadline(limit)
		}
	}
	r.broadcast()
	return nil
}

// pin: the player moved their pin but has not pressed Guess. If the round ends before they do
// (their tab was asleep, the phone locked, the timer message came late), the pin counts.
func (r *Room) pin(p *Player, pos *LatLon, hint bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhasePlaying {
		return errWrongPhase
	}
	rec := r.rounds[len(r.rounds)-1]
	if _, done := rec.Guesses[p.ID]; done {
		return errWrongPhase
	}
	if pos == nil || !pos.insideBox() {
		delete(rec.pins, p.ID) // a pin far outside the country scores 0 anyway
		return nil
	}
	rec.pins[p.ID] = &Guess{Pos: pos, Hint: hint}
	return nil
}

// next: the host moves on from a round result to the next round or the final table.
func (r *Room) next(p *Player) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseResult {
		return errWrongPhase
	}
	if r.round >= r.Settings.Rounds {
		r.phase = PhaseFinal
	} else {
		r.round++
		r.phase = PhasePicking
	}
	r.broadcast()
	return nil
}

func (r *Room) setDeadline(t time.Time) {
	r.deadline = t
	if r.timer != nil {
		r.timer.Stop()
	}
	round := r.round
	r.timer = time.AfterFunc(time.Until(t)+deadlineGrace, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.phase == PhasePlaying && r.round == round {
			r.endRound()
		}
	})
}

func (r *Room) allOnlineGuessed() bool {
	rec := r.rounds[len(r.rounds)-1]
	online := 0
	for _, p := range r.players {
		if p.out == nil {
			continue
		}
		online++
		if _, ok := rec.Guesses[p.ID]; !ok {
			return false
		}
	}
	return online > 0
}

// endRound scores every player. Anyone who did not press Guess is scored by their last pin,
// or gets 0 without one.
func (r *Room) endRound() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.deadline = time.Time{}
	rec := r.rounds[len(r.rounds)-1]
	for _, p := range r.players {
		g, ok := rec.Guesses[p.ID]
		if !ok {
			if g = rec.pins[p.ID]; g == nil {
				g = &Guess{}
			}
			rec.Guesses[p.ID] = g
		}
		if g.Pos != nil {
			km := distanceKm(rec.Actual, *g.Pos)
			g.Km = &km
			g.Score = roundScore(r.Settings, km, g.Hint)
		}
		p.Total += g.Score
	}
	rec.done = true
	rec.pins = nil
	r.phase = PhaseResult
	r.broadcast()
}

// afterChange runs after someone joins, leaves or drops: fixes the host and the round state.
func (r *Room) afterChange() {
	online := 0
	for _, p := range r.players {
		if p.out != nil {
			online++
		}
	}
	if online == 0 {
		if r.emptySince.IsZero() {
			r.emptySince = time.Now()
		}
	} else {
		r.emptySince = time.Time{}
	}

	if h := r.player(r.hostID); h == nil || h.out == nil {
		for _, p := range r.players {
			if p.out != nil {
				r.hostID = p.ID
				break
			}
		}
	}

	if r.phase == PhasePlaying && r.allOnlineGuessed() {
		r.endRound() // broadcasts
		return
	}
	r.broadcast()
}

func (r *Room) player(id string) *Player {
	for _, p := range r.players {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (r *Room) idleFor(now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.emptySince.IsZero() {
		return 0
	}
	return now.Sub(r.emptySince)
}

// ---------- what the browsers see ----------

// welcomeMsg tells a browser which player it is and the token to rejoin with.
func welcomeMsg(code string, p *Player) []byte {
	msg, _ := json.Marshal(map[string]string{"t": "welcome", "code": code, "you": p.ID, "token": p.Token})
	return msg
}

type playerView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Color   int    `json:"color"`
	Online  bool   `json:"online"`
	Total   int    `json:"total"`
	Guessed bool   `json:"guessed"`
}

type stateMsg struct {
	T           string         `json:"t"`
	Code        string         `json:"code"`
	Phase       Phase          `json:"phase"`
	HostID      string         `json:"hostId"`
	Settings    Settings       `json:"settings"`
	Round       int            `json:"round"`
	Players     []playerView   `json:"players"`
	Pano        *LatLon        `json:"pano,omitempty"`        // current round, while playing
	RemainingMs int64          `json:"remainingMs,omitempty"` // while playing with a deadline
	History     []*RoundRecord `json:"history"`               // finished rounds, with every guess
}

func (r *Room) snapshot() stateMsg {
	m := stateMsg{
		T:        "state",
		Code:     r.Code,
		Phase:    r.phase,
		HostID:   r.hostID,
		Settings: r.Settings,
		Round:    r.round,
		History:  []*RoundRecord{},
	}
	var current *RoundRecord
	if n := len(r.rounds); n > 0 {
		current = r.rounds[n-1]
	}
	for _, p := range r.players {
		v := playerView{ID: p.ID, Name: p.Name, Color: p.Color, Online: p.out != nil, Total: p.Total}
		if current != nil && r.phase == PhasePlaying {
			_, v.Guessed = current.Guesses[p.ID]
		}
		m.Players = append(m.Players, v)
	}
	for _, rec := range r.rounds {
		if rec.done {
			m.History = append(m.History, rec)
		}
	}
	if r.phase == PhasePlaying && current != nil {
		pos := current.Actual
		m.Pano = &pos
		if !r.deadline.IsZero() {
			m.RemainingMs = max(time.Until(r.deadline).Milliseconds(), 1)
		}
	}
	return m
}

func (r *Room) broadcast() {
	msg, err := json.Marshal(r.snapshot())
	if err != nil {
		return
	}
	for _, p := range r.players {
		if p.out != nil {
			p.out.queue(msg)
		}
	}
}
