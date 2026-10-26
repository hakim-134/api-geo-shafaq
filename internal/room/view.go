package room

import (
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/score"
)

// View is the room as one player sees it: everyone's names and totals, the round's panorama,
// and only their own hints and pin.
type View struct {
	Code     string
	Ver      int64
	Phase    Phase
	You      uuid.UUID
	Host     uuid.UUID
	Map      *maps.Map
	Rules    rules.Rules
	Games    int
	YourGame *uuid.UUID // the player's games row of this game, once a round of it is saved
	GameID   *uuid.UUID // the room game, while one is on or just over
	Players  []PlayerView
	Round    *RoundView
	Results  []ResultView
	Max      int
	Blocked  error // the caller is not in the room and cannot join it: ErrKicked, ErrFull
}

type PlayerView struct {
	ID      uuid.UUID
	Name    string
	Avatar  string
	Guest   bool
	Online  bool
	Left    bool
	Total   int
	Guessed bool // in the current round
}

type RoundView struct {
	N         int
	Swap      int
	View      provider.View
	Shown     bool
	Remaining *time.Duration
	Hints     []game.Hint
	HintsLeft int
	Guessed   bool
	Pin       *geo.Point
	Answered  int // players of the round who answered
	Players   int // players of the round
}

type ResultView struct {
	N      int
	Actual geo.Point
	Place  places.Info
	Scores []Score
}

// View is the room for one player (anyone may look: a page opened by a link before joining).
func (r *Room) View(id uuid.UUID) *View {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view(id)
}

func (r *Room) view(id uuid.UUID) *View {
	v := &View{
		Code: r.st.Code, Ver: r.st.Ver, Phase: r.st.Phase, You: id, Host: r.st.Host,
		Map: r.m, Rules: r.st.Rules, Games: r.st.Games,
		Players: make([]PlayerView, 0, len(r.st.Players)), Results: make([]ResultView, 0, len(r.st.Done)),
	}
	if p := r.player(id); p == nil || p.Left {
		if slices.Contains(r.st.Kicked, id) {
			v.Blocked = ErrKicked
		} else if len(r.active()) >= MaxPlayers {
			v.Blocked = ErrFull
		}
	}
	if g, ok := r.st.GameRows[id]; ok {
		v.YourGame = &g
	}
	if r.st.Games > 0 {
		v.GameID = &r.st.GameID
	}
	rd := r.st.Round
	if r.st.Phase != Playing {
		rd = nil
	}
	for _, p := range r.st.Players {
		pv := PlayerView{
			ID: p.ID, Name: p.Name, Avatar: p.Avatar, Guest: p.Guest, Online: p.conns > 0,
			Left: p.Left, Total: p.Total,
		}
		if rd != nil {
			pv.Guessed = rd.Guesses[p.ID] != nil
		}
		v.Players = append(v.Players, pv)
	}
	if rd != nil {
		rv := &RoundView{N: rd.N, Swap: rd.Swap, Shown: rd.ShownAt != nil, Hints: orEmpty(rd.Hints[id])}
		if p, ok := r.hub.Games.Reg.Get(rd.Provider); ok {
			rv.View = p.View(provider.Pano{ID: rd.PanoID, Provider: rd.Provider, Ref: rd.Ref, Pos: rd.Library}, rd.Near, rd.SnapKm, rd.Heading)
		} else {
			rv.View = provider.View{Provider: rd.Provider}
		}
		if rd.Deadline != nil {
			left := max(rd.Deadline.Sub(r.now()), 0)
			rv.Remaining = &left
		}
		used := 0
		if p := r.player(id); p != nil {
			used = p.HintsUsed
		}
		rv.HintsLeft = r.st.Rules.HintsLeft(len(rv.Hints), used)
		rv.Guessed = rd.Guesses[id] != nil
		if pin, ok := rd.Pins[id]; ok && !rv.Guessed {
			rv.Pin = &pin
		} else if g := rd.Guesses[id]; g != nil {
			rv.Pin = g.Pos
		}
		for pid := range rd.In {
			if p := r.player(pid); p != nil && !p.Left {
				rv.Players++
				if rd.Guesses[pid] != nil {
					rv.Answered++
				}
			}
		}
		v.Round = rv
	}
	for _, d := range r.st.Done {
		v.Results = append(v.Results, ResultView{N: d.Round.N, Actual: d.Round.Actual, Place: d.Place, Scores: d.Scores})
	}
	if r.st.Rules.Endless() {
		v.Max = len(r.st.Done) * score.Max
	} else {
		v.Max = r.st.Rules.Rounds * score.Max
	}
	return v
}
