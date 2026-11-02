package game

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// codeLetters leave out what reads alike over a phone or on a card (0/O, 1/I), as room codes do.
const (
	codeLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLen     = 6
)

func newCode() string {
	b := make([]byte, codeLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeLetters[int(b[i])%len(codeLetters)]
	}
	return string(b)
}

// NormalCode is a code as it is stored: a link typed by hand may be in small letters.
func NormalCode(code string) string { return strings.ToUpper(code) }

// CreateChallenge makes the link for a finished game of the player: its map, rules and panoramas.
// Asked again, by the player or by another player of the same room game, it gives the same one.
// The game's rows stand on the leaderboard from the start, so friends have someone to beat.
func (s *Service) CreateChallenge(ctx context.Context, userID, gameID uuid.UUID) (string, error) {
	var code string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.GetGame(ctx, gameID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.UserID != userID) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Status != "finished" || row.Played == 0 {
			return ErrWrongPhase
		}
		if row.Challenge != nil {
			code = *row.Challenge
			return nil
		}
		source := row.ID
		if row.RoomGame != nil {
			source = *row.RoomGame
		}
		for range 5 {
			made, err := q.InsertChallenge(ctx, store.InsertChallengeParams{
				Code: newCode(), MapID: row.MapID, Rules: row.Rules, SizeKm: row.SizeKm, Seed: row.Seed,
				Source: source, CreatedBy: userID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				have, err := q.ChallengeBySource(ctx, source)
				if err == nil { // another player of the room was first
					code = have.Code
					return q.LinkChallengeGames(ctx, store.LinkChallengeGamesParams{Code: &code, Source: source})
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				continue // the code was taken: another one
			}
			if err != nil {
				return err
			}
			code = made.Code
			n, err := q.CopyChallengeRounds(ctx, store.CopyChallengeRoundsParams{Code: code, Source: source})
			if err != nil {
				return err
			}
			// an endless game, or one left early: the challenge is the rounds that were played
			var rs rules.Rules
			if err := json.Unmarshal(row.Rules, &rs); err != nil {
				return err
			}
			rs.Rounds = int(n)
			rulesJSON, _ := json.Marshal(rs)
			if err := q.SetChallengeRules(ctx, store.SetChallengeRulesParams{Code: code, Rules: rulesJSON}); err != nil {
				return err
			}
			return q.LinkChallengeGames(ctx, store.LinkChallengeGamesParams{Code: &code, Source: source})
		}
		return errors.New("no free challenge code")
	})
	return code, err
}

// PlayChallenge is the player's game of a challenge: one each, so a second try can't climb the
// leaderboard with the streets already known. It starts the game when there is none; the same
// game comes back after a reload, and a finished one after that. The challenge's row is locked,
// so two taps cannot start two games.
func (s *Service) PlayChallenge(ctx context.Context, userID uuid.UUID, code string) (*Game, error) {
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ch, err := q.LockChallenge(ctx, code)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		have, err := q.PlayerChallengeGame(ctx, store.PlayerChallengeGameParams{Challenge: &code, UserID: userID})
		if err == nil {
			id = have.ID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.challengeOpen(ctx, q, ch); err != nil {
			return err
		}
		row, err := q.CreateChallengeGame(ctx, store.CreateChallengeGameParams{
			UserID: userID, MapID: ch.MapID, Rules: ch.Rules, SizeKm: ch.SizeKm, Seed: ch.Seed, Challenge: &code,
		})
		if err != nil {
			return err
		}
		id = row.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id)
}

// challengeOpen checks that a new game of the challenge can be played today: every round's
// provider is on, and none has reached today's cap (only new games are turned away, as ever).
func (s *Service) challengeOpen(ctx context.Context, q *store.Queries, ch store.Challenge) error {
	var rs rules.Rules
	if err := json.Unmarshal(ch.Rules, &rs); err != nil {
		return err
	}
	allowed := s.Reg.Allowed(rs.Providers)
	open, err := s.uncapped(ctx, allowed)
	if err != nil {
		return err
	}
	rounds, err := q.ChallengeRounds(ctx, ch.Code)
	if err != nil {
		return err
	}
	if len(rounds) == 0 {
		return ErrNoPanorama
	}
	for _, r := range rounds {
		if !allowed[r.Provider] {
			return ErrNoPanorama
		}
		if !open[r.Provider] {
			return ErrDailyCap
		}
	}
	return nil
}

// challengeRound is round n's fixed panorama: the same point and first heading for everyone.
func challengeRound(ctx context.Context, q *store.Queries, code string, n int) (c maps.Candidate, near geo.Point, heading, snapKm float64, err error) {
	rows, err := q.ChallengeRounds(ctx, code)
	if err != nil {
		return c, near, 0, 0, err
	}
	if n < 1 || n > len(rows) {
		return c, near, 0, 0, ErrNoPanorama
	}
	r := rows[n-1]
	c = maps.Candidate{Pano: provider.Pano{ID: r.PanoramaID, Provider: r.Provider, Ref: r.Ref}}
	return c, geo.Point{Lat: r.NearLat, Lon: r.NearLon}, float64(r.Heading), r.SnapKm, nil
}

// challengeCrowd is a challenge round's ring: its players that round, two guesses or more, like a
// room's. nil for any other game, and when it can't be read: the result stands without it.
func (s *Service) challengeCrowd(ctx context.Context, id uuid.UUID, n int) *Crowd {
	q := store.New(s.Pool)
	g, err := q.GetGame(ctx, id)
	if err != nil || g.Challenge == nil {
		return nil
	}
	row, err := q.ChallengeCrowd(ctx, store.ChallengeCrowdParams{Challenge: g.Challenge, GameID: id, N: int32(n)})
	if err != nil || row.Players < 2 {
		return nil
	}
	return &Crowd{Km: row.Km, N: int(row.Players)}
}

// BoardRow is one player on a challenge's leaderboard.
type BoardRow struct {
	Place int
	User  store.User
	Game  uuid.UUID
	Total int
	Hints int // hints taken, when they cost points
}

// Challenge is a challenge as a page shows it.
type Challenge struct {
	Code    string
	Map     *maps.Map
	Rules   rules.Rules
	By      store.User
	Players int
	Board   []BoardRow // everyone who finished it, best first
	Rounds  int
	You     *BoardYou
}

// BoardYou is the caller's game of a challenge.
type BoardYou struct {
	Game   uuid.UUID
	Status string
	Total  int
	Place  int // once finished
}

// GetChallenge reads a challenge and its leaderboard. userID may be the zero user: no You.
func (s *Service) GetChallenge(ctx context.Context, userID uuid.UUID, code string) (*Challenge, error) {
	q := store.New(s.Pool)
	ch, err := q.GetChallenge(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m, err := s.Catalog.ByID(ctx, ch.MapID)
	if err != nil {
		return nil, err
	}
	by, err := q.GetUser(ctx, ch.CreatedBy)
	if err != nil {
		return nil, err
	}
	out := &Challenge{Code: code, Map: m, By: by}
	if err := json.Unmarshal(ch.Rules, &out.Rules); err != nil {
		return nil, err
	}
	out.Rounds = out.Rules.Rounds
	rows, err := q.ChallengeBoard(ctx, &code)
	if err != nil {
		return nil, err
	}
	out.Players = len(rows)
	out.Board = make([]BoardRow, len(rows))
	for i, r := range rows {
		place := i + 1
		if i > 0 && r.Total == rows[i-1].Total { // a tie shares the place
			place = out.Board[i-1].Place
		}
		out.Board[i] = BoardRow{Place: place, User: r.User, Game: r.GameID, Total: int(r.Total), Hints: CostlyHints(r.Rules, int(r.HintsUsed))}
	}
	mine, err := q.PlayerChallengeGame(ctx, store.PlayerChallengeGameParams{Challenge: &code, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.You = &BoardYou{Game: mine.ID, Status: mine.Status, Total: int(mine.Total)}
	for _, b := range out.Board {
		if b.User.ID == userID {
			out.You.Place = b.Place
		}
	}
	return out, nil
}

// CreatorScores are the challenger's round scores, for the card's squares.
func (s *Service) CreatorScores(ctx context.Context, ch *Challenge) ([]int, error) {
	rows, err := store.New(s.Pool).ChallengeCreatorRounds(ctx, store.ChallengeCreatorRoundsParams{Challenge: &ch.Code, UserID: ch.By.ID})
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, ch.Rounds)
	for _, sc := range rows {
		if len(out) == ch.Rounds {
			break
		}
		out = append(out, int(sc))
	}
	return out, nil
}
