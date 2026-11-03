package game

import (
	"context"
	"errors"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/barg"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// spotOf is where a round's panorama really is: the guess that finds it.
func spotOf(t *testing.T, f *fixture, id any, n int) geo.Point {
	t.Helper()
	var at geo.Point
	if err := f.pool.QueryRow(context.Background(), `SELECT ST_Y(p.pos), ST_X(p.pos) FROM rounds r JOIN panoramas p ON p.id = r.panorama_id
		WHERE r.game_id = $1 AND r.n = $2`, id, n).Scan(&at.Lat, &at.Lon); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestLedgerIsAppendOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	q := store.New(f.pool)
	if ok, err := barg.Earn(ctx, q, f.user, barg.ReasonStreet, "1", "Ko‘cha", 5); !ok || err != nil {
		t.Fatalf("first earning: %v %v", ok, err)
	}
	if ok, err := barg.Earn(ctx, q, f.user, barg.ReasonStreet, "1", "Ko‘cha", 5); ok || err != nil {
		t.Fatalf("the same earning again: %v %v", ok, err)
	}
	if b, _ := q.BargBalance(ctx, f.user); b != 5 {
		t.Fatalf("balance %d, want 5", b)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE barg_ledger SET amount = 1000`); err == nil {
		t.Fatal("a ledger row was edited")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM barg_ledger`); err == nil {
		t.Fatal("a ledger row was deleted")
	}
}

func TestBuyAndWear(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	q := store.New(f.pool)
	if err := barg.Buy(ctx, f.pool, f.user, "frame-zar"); !errors.Is(err, barg.ErrNotEnough) {
		t.Fatalf("buying with nothing: %v", err)
	}
	if err := barg.Buy(ctx, f.pool, f.user, "no-such"); !errors.Is(err, barg.ErrNoItem) {
		t.Fatalf("an item that isn't there: %v", err)
	}
	if _, err := barg.Earn(ctx, q, f.user, barg.ReasonDistrict, "x", "", 150); err != nil {
		t.Fatal(err)
	}
	for range 2 { // a second tap changes nothing
		if err := barg.Buy(ctx, f.pool, f.user, "frame-zar"); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := q.BargBalance(ctx, f.user); b != 50 {
		t.Fatalf("balance %d after 150 − 100, want 50", b)
	}
	owned, _ := q.OwnedItems(ctx, f.user)
	if len(owned) != 1 {
		t.Fatalf("owns %v", owned)
	}
}

// Only learning earns: a far guess nothing, a close one once, a second close one nothing.
func TestAGuessEarnsOnlyByLearning(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	twoStreets(t, f)
	q := store.New(f.pool)
	g, err := f.svc.Create(ctx, f.user, "tashkent-streets", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	far := geo.Point{Lat: 40, Lon: 60}
	res, err := f.svc.Guess(ctx, f.user, g.ID, 1, &far)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Earned) != 0 {
		t.Fatalf("a far guess earned %+v", res.Earned)
	}
	if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
		t.Fatal(err)
	}
	at := spotOf(t, f, g.ID, 2)
	res, err = f.svc.Guess(ctx, f.user, g.ID, 2, &at)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Earned) != 1 || res.Earned[0].Reason != barg.ReasonStreet || res.Earned[0].Amount != barg.AmountStreet {
		t.Fatalf("a street learned earned %+v", res.Earned)
	}
	// asked again, the same answer earns nothing more
	again, err := f.svc.Guess(ctx, f.user, g.ID, 2, &at)
	if err != nil || len(again.Earned) != 0 {
		t.Fatalf("the same guess again: %+v %v", again.Earned, err)
	}
	if b, _ := q.BargBalance(ctx, f.user); b != barg.AmountStreet {
		t.Fatalf("balance %d", b)
	}
}

// Every street of a district learned earns the district, once.
func TestADistrictLearnedEarns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// the west district: its five streets, one panorama each; everything else out of play
	for _, sql := range []string{
		`INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('west-map', 'G‘arb', '{"rounds":5}', '{"group":"street","snapKm":0.12,"streetsOnly":true}')`,
		`INSERT INTO map_sources (map_id, place_id) SELECT m.id, p.id FROM maps m, places p WHERE m.slug = 'west-map' AND p.slug = 'west'`,
		`UPDATE panoramas SET status = 'gone' WHERE id NOT IN (
			SELECT p.id FROM panoramas p JOIN places pl ON pl.id = ANY (p.place_ids) WHERE pl.slug = 'west' AND p.provider = 'demo' ORDER BY p.id LIMIT 5)
			AND provider = 'demo'`,
		`UPDATE panoramas SET status = 'gone' WHERE provider = 'yandex' AND id NOT IN (
			SELECT p.id FROM panoramas p JOIN places pl ON pl.id = ANY (p.place_ids) WHERE pl.slug = 'west' AND p.provider = 'yandex' ORDER BY p.id LIMIT 5)`,
		`INSERT INTO streets (place_id, name_uz) SELECT pl.id, 'K' || g FROM places pl, generate_series(1, 5) g WHERE pl.slug = 'west'`,
		`UPDATE panoramas p SET street_id = (SELECT s.id FROM streets s WHERE s.name_uz = 'K' || (1 + (SELECT count(*) FROM panoramas q
			WHERE q.provider = p.provider AND q.status = 'active' AND q.id < p.id))) WHERE p.status = 'active'`,
	} {
		if _, err := f.pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	r := testRules("demo")
	r.Rounds = 5
	r.Difficulty = rules.AnyPlace
	g, err := f.svc.Create(ctx, f.user, "west-map", r)
	if err != nil {
		t.Fatal(err)
	}
	var earned []barg.Earned
	for n := 1; n <= g.Rules.Rounds; n++ {
		if _, err := f.svc.NextRound(ctx, f.user, g.ID); err != nil {
			t.Fatal(err)
		}
		at := spotOf(t, f, g.ID, n)
		res, err := f.svc.Guess(ctx, f.user, g.ID, n, &at)
		if err != nil {
			t.Fatal(err)
		}
		earned = append(earned, res.Earned...)
	}
	streets, districts := 0, 0
	for _, e := range earned {
		switch e.Reason {
		case barg.ReasonStreet:
			streets++
		case barg.ReasonDistrict:
			districts++
		}
	}
	if streets != 5 || districts != 1 {
		t.Fatalf("earned %d streets, %d districts: %+v", streets, districts, earned)
	}
}

// First on a challenge's board, against someone else, earns; the challenger's own game doesn't.
func TestAChallengeWonEarns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	g, err := f.svc.Create(ctx, f.other, "city", testRules("demo"))
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		res := playRound(t, f2(f), g.ID, n)
		if len(res.Earned) != 0 && res.GameOver {
			t.Fatalf("the challenger earned %+v by playing alone", res.Earned)
		}
	}
	code, err := f.svc.CreateChallenge(ctx, f.other, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := f.svc.PlayChallenge(ctx, f.user, code)
	if err != nil {
		t.Fatal(err)
	}
	var last *Result
	for n := 1; n <= 2; n++ {
		if _, err := f.svc.NextRound(ctx, f.user, mine.ID); err != nil {
			t.Fatal(err)
		}
		at := spotOf(t, f, mine.ID, n)
		if last, err = f.svc.Guess(ctx, f.user, mine.ID, n, &at); err != nil {
			t.Fatal(err)
		}
	}
	won := false
	for _, e := range last.Earned {
		won = won || e.Reason == barg.ReasonChallenge
	}
	if !won {
		t.Fatalf("first on the board earned %+v", last.Earned)
	}
}

// f2 is the fixture with the other player as its player.
func f2(f *fixture) *fixture {
	c := *f
	c.user, c.other = f.other, f.user
	return &c
}

func TestMergeMovesTheLedger(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	q := store.New(f.pool)
	if _, err := barg.Earn(ctx, q, f.user, barg.ReasonDistrict, "x", "", 150); err != nil {
		t.Fatal(err)
	}
	if err := barg.Buy(ctx, f.pool, f.user, "frame-zar"); err != nil {
		t.Fatal(err)
	}
	if err := q.MergeBarg(ctx, store.MergeBargParams{KeepID: f.other, OtherID: f.user}); err != nil {
		t.Fatal(err)
	}
	if err := q.MergeOwned(ctx, store.MergeOwnedParams{KeepID: f.other, OtherID: f.user}); err != nil {
		t.Fatal(err)
	}
	if b, _ := q.BargBalance(ctx, f.other); b != 50 {
		t.Fatalf("balance after the merge %d, want 50", b)
	}
	// earned before the merge: not again after it
	if ok, err := barg.Earn(ctx, q, f.other, barg.ReasonDistrict, "x", "", 150); ok || err != nil {
		t.Fatalf("earned twice through a merge: %v %v", ok, err)
	}
}
