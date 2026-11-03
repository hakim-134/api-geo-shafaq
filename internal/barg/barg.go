// Package barg is the game's one currency and its shop (PLAN 9.3, 9.4). A player earns Barg by
// learning and playing well, never by time spent, and spends it on looks at fixed prices. Nothing
// is sold for money yet, nothing is random, and nothing changes how a game scores (players.md).
package barg

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// What earns Barg, and how much.
const (
	ReasonStreet    = "street"    // a street guessed within 500 m for the first time
	ReasonDistrict  = "district"  // every street or spot of a district learned
	ReasonChallenge = "challenge" // first on a challenge's board, against someone else
	ReasonBuy       = "buy"
	ReasonRefund    = "refund" // an item that left the shop: the ledger is never edited, so it pays back

	AmountStreet    = 5
	AmountDistrict  = 50
	AmountChallenge = 20

	// ChallengeWinsADay: two guests on one phone shouldn't make a Barg machine.
	ChallengeWinsADay = 3
)

// Errors with the stable codes the API returns.
var (
	ErrNoItem      = errors.New("not_found")
	ErrNotEnough   = errors.New("not_enough_barg")
	ErrNotOwned    = errors.New("not_owned")
	ErrNotWearable = errors.New("bad_input")
)

// Kinds of things the shop has.
const (
	KindFrame = "frame"
	KindPin   = "pin"
	KindTrail = "trail"
)

// Item is one thing in the shop, at a fixed price. Its name and look are the game's.
type Item struct {
	ID    string
	Kind  string
	Price int
}

// Shop is everything for sale, in the order it shows. Compass skins and room emotes were tried and
// taken out for now (the owner, 2026-11-03): decision 0021.
var Shop = []Item{
	{ID: "frame-zar", Kind: KindFrame, Price: 100},
	{ID: "frame-lojuvard", Kind: KindFrame, Price: 120},
	{ID: "frame-anor", Kind: KindFrame, Price: 120},
	{ID: "frame-girih", Kind: KindFrame, Price: 200},
	{ID: "frame-samarqand", Kind: KindFrame, Price: 300},
	{ID: "pin-zar", Kind: KindPin, Price: 60},
	{ID: "pin-anor", Kind: KindPin, Price: 60},
	{ID: "pin-lojuvard", Kind: KindPin, Price: 60},
	{ID: "trail-zar", Kind: KindTrail, Price: 60},
	{ID: "trail-lojuvard", Kind: KindTrail, Price: 60},
	{ID: "trail-nuqta", Kind: KindTrail, Price: 80},
}

// Find is an item of the shop by its id.
func Find(id string) (Item, bool) {
	i := slices.IndexFunc(Shop, func(x Item) bool { return x.ID == id })
	if i < 0 {
		return Item{}, false
	}
	return Shop[i], true
}

// Earned is one earning, to say so to the player when it happens.
type Earned struct {
	Reason string `json:"reason"`
	Amount int    `json:"amount"`
	Note   string `json:"note,omitempty"`
}

// Earn writes an earning once; false when it was earned before.
func Earn(ctx context.Context, q *store.Queries, user uuid.UUID, reason, ref, note string, amount int) (bool, error) {
	n, err := q.EarnBarg(ctx, store.EarnBargParams{
		UserID: user, Amount: int32(amount), Reason: reason, Ref: ref, Note: note,
		Key: fmt.Sprintf("%s:%s:%s", reason, ref, user),
	})
	return n > 0, err
}

// Buy spends the price and adds the item; bought before, it changes nothing. The player's row is
// held meanwhile, so two taps can't both spend the same Barg.
func Buy(ctx context.Context, pool *pgxpool.Pool, user uuid.UUID, id string) error {
	it, ok := Find(id)
	if !ok {
		return ErrNoItem
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockUser(ctx, user); err != nil {
			return err
		}
		owned, err := q.OwnedItems(ctx, user)
		if err != nil {
			return err
		}
		if slices.Contains(owned, id) {
			return nil
		}
		balance, err := q.BargBalance(ctx, user)
		if err != nil {
			return err
		}
		if int(balance) < it.Price {
			return ErrNotEnough
		}
		if _, err := Earn(ctx, q, user, ReasonBuy, id, "", -it.Price); err != nil {
			return err
		}
		return q.AddOwned(ctx, store.AddOwnedParams{UserID: user, Item: id})
	})
}
