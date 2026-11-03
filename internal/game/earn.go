package game

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/barg"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// AwardGuess is what a guess earns, once it is saved: a street learned for the first time, and a
// district when that was the last of it. A failure costs the player nothing they can't earn again
// on the next close guess, so it never fails the guess itself.
func (s *Service) AwardGuess(ctx context.Context, user uuid.UUID, panorama int64, km *float64) []barg.Earned {
	if km == nil || *km > places.LearnedKm {
		return nil
	}
	out, err := s.awardGuess(ctx, user, panorama)
	if err != nil {
		slog.WarnContext(ctx, "barg not earned", "user", user, "panorama", panorama, "err", err)
	}
	return out
}

func (s *Service) awardGuess(ctx context.Context, user uuid.UUID, panorama int64) ([]barg.Earned, error) {
	q := store.New(s.Pool)
	p, err := q.PanoramaForBarg(ctx, panorama)
	if err != nil {
		return nil, err
	}
	var out []barg.Earned
	if p.StreetID != 0 {
		ok, err := barg.Earn(ctx, q, user, barg.ReasonStreet, strconv.FormatInt(p.StreetID, 10), p.Street, barg.AmountStreet)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, barg.Earned{Reason: barg.ReasonStreet, Amount: barg.AmountStreet, Note: p.Street})
		}
	}
	tree, err := s.Catalog.Tree(ctx)
	if err != nil {
		return out, err
	}
	rounds, err := places.UserRounds(ctx, q, user)
	if err != nil {
		return out, err
	}
	for _, id := range p.PlaceIds {
		pl := tree.ByID(id)
		if pl == nil || pl.Kind != "district" {
			continue
		}
		if pr := pl.Progress(rounds); pr.Total == 0 || pr.Learned < pr.Total || pr.Unit == "" {
			continue
		}
		ok, err := barg.Earn(ctx, q, user, barg.ReasonDistrict, pl.Slug, pl.Name.Uz, barg.AmountDistrict)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, barg.Earned{Reason: barg.ReasonDistrict, Amount: barg.AmountDistrict, Note: pl.Name.Uz})
		}
	}
	return out, nil
}

// AwardChallenge: the player just finished a challenge and stands first, with someone else on the
// board who started it. A win once per challenge, a few a day.
func (s *Service) AwardChallenge(ctx context.Context, user uuid.UUID, code string) []barg.Earned {
	out, err := s.awardChallenge(ctx, user, code)
	if err != nil {
		slog.WarnContext(ctx, "barg not earned", "user", user, "challenge", code, "err", err)
	}
	return out
}

func (s *Service) awardChallenge(ctx context.Context, user uuid.UUID, code string) ([]barg.Earned, error) {
	q := store.New(s.Pool)
	board, err := q.ChallengeBoard(ctx, &code)
	if err != nil {
		return nil, err
	}
	if len(board) < 2 || board[0].User.ID != user {
		return nil, nil
	}
	wins, err := q.BargEarnedSince(ctx, store.BargEarnedSinceParams{
		UserID: user, Reason: barg.ReasonChallenge, Since: s.now().Add(-24 * time.Hour),
	})
	if err != nil || int(wins) >= barg.ChallengeWinsADay {
		return nil, err
	}
	ok, err := barg.Earn(ctx, q, user, barg.ReasonChallenge, code, "", barg.AmountChallenge)
	if err != nil || !ok {
		return nil, err
	}
	return []barg.Earned{{Reason: barg.ReasonChallenge, Amount: barg.AmountChallenge}}, nil
}
