// Package rules is the one rule set for solo games, rooms and challenges. It is stored as
// JSON (maps' defaults, every game) and sent to the game as is.
package rules

import (
	"errors"
	"slices"
)

type Movement string

const (
	Move   Movement = "move"
	NoMove Movement = "nomove"
	NMPZ   Movement = "nmpz" // no move, no pan, no zoom
)

type HintPer string

const (
	PerGame  HintPer = "game"
	PerRound HintPer = "round"
)

type Hints struct {
	Count int     `json:"count"` // 0 = no Hint button
	Per   HintPer `json:"per"`
	Cost  float64 `json:"cost"` // share of the round score each hint takes
}

type Rules struct {
	Rounds     int      `json:"rounds"`
	TimeSec    int      `json:"timeSec"` // 0 = no limit
	Movement   Movement `json:"movement"`
	Hints      Hints    `json:"hints"`
	Compass    bool     `json:"compass"`
	Providers  []string `json:"providers"` // empty = any
	ScoreScale float64  `json:"scoreScale"`
	PanoLabels bool     `json:"panoLabels"`
	MapLabels  bool     `json:"mapLabels"`
}

const (
	MaxRounds  = 20
	MinTimeSec = 10
	MaxTimeSec = 600
	MaxHints   = 20
)

var ErrInvalid = errors.New("invalid rules")

// Validate checks the ranges. known lists the provider names the server has.
func (r Rules) Validate(known []string) error {
	switch {
	case r.Rounds < 1 || r.Rounds > MaxRounds,
		r.TimeSec != 0 && (r.TimeSec < MinTimeSec || r.TimeSec > MaxTimeSec),
		r.Movement != Move && r.Movement != NoMove && r.Movement != NMPZ,
		r.Hints.Count < 0 || r.Hints.Count > MaxHints,
		r.Hints.Per != PerGame && r.Hints.Per != PerRound,
		r.Hints.Cost < 0 || r.Hints.Cost > 1,
		r.ScoreScale < 0.25 || r.ScoreScale > 4,
		len(r.Providers) > len(known):
		return ErrInvalid
	}
	for _, p := range r.Providers {
		if !slices.Contains(known, p) {
			return ErrInvalid
		}
	}
	return nil
}

// HintsLeft is how many hints the player still has in this round, given those spent in the
// round and in the whole game.
func (r Rules) HintsLeft(inRound, inGame int) int {
	used := inGame
	if r.Hints.Per == PerRound {
		used = inRound
	}
	return max(r.Hints.Count-used, 0)
}

// The v0 difficulties, kept as presets for the menu.
var (
	Easy = Rules{
		Rounds: 5, Movement: Move, Compass: true, Providers: []string{},
		Hints:      Hints{Count: 1, Per: PerRound, Cost: 0},
		ScoreScale: 2, PanoLabels: true, MapLabels: true,
	}
	Normal = Rules{
		Rounds: 5, Movement: Move, Compass: true, Providers: []string{},
		Hints:      Hints{Count: 1, Per: PerRound, Cost: 0.2},
		ScoreScale: 1, MapLabels: true,
	}
	Hard = Rules{
		Rounds: 5, Movement: Move, Compass: true, Providers: []string{},
		Hints:      Hints{Count: 0, Per: PerRound},
		ScoreScale: 0.6,
	}
)
