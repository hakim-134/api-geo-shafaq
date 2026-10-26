package players

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
	"github.com/hakim-134/api-geo-shafaq/internal/testdb"
)

func TestAccounts(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	q := store.New(pool)
	s := &Service{Pool: pool, Q: q, TTL: time.Hour}
	var mapID int64
	if err := pool.QueryRow(ctx, `INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('m', 'M', '{}', '{}') RETURNING id`).Scan(&mapID); err != nil {
		t.Fatal(err)
	}
	play := func(u uuid.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO games (user_id, map_id, rules, size_km, seed, status) VALUES ($1, $2, '{"rounds":5}', 10, 1, 'finished')`, u, mapID); err != nil {
			t.Fatal(err)
		}
	}
	games := func(u uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM games WHERE user_id = $1`, u).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	guest := func() (store.User, string) {
		t.Helper()
		u, token, err := s.NewGuest(ctx, "test")
		if err != nil {
			t.Fatal(err)
		}
		return u, token
	}
	tg := auth.TelegramUser{ID: 42, FirstName: "Ali", Username: "ali"}.Identity()
	google := auth.Identity{Provider: "google", Subject: "g-1", Name: "Ali", Username: "ali@example.com"}

	// A guest plays on the phone, then signs in with Telegram through the bot.
	phone, phoneToken := guest()
	play(phone.ID)
	code, err := s.StartTelegram(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.CheckTelegram(ctx, phone, "test", code); err != nil || out.Status != Pending {
		t.Fatalf("before Start: %+v, %v", out, err)
	}
	if ok, err := s.ConfirmTelegram(ctx, code, auth.TelegramUser{ID: 42, FirstName: "Ali", Username: "ali"}); !ok || err != nil {
		t.Fatalf("confirm: %v, %v", ok, err)
	}
	if ok, _ := s.ConfirmTelegram(ctx, code, auth.TelegramUser{ID: 99}); ok {
		t.Fatal("a code was confirmed twice")
	}
	out, err := s.CheckTelegram(ctx, phone, "test", code)
	if err != nil || out.Status != Linked || out.User.Guest {
		t.Fatalf("sign-in: %+v, %v", out, err)
	}
	if _, err := s.CheckTelegram(ctx, phone, "test", code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code was used twice: %v", err)
	}

	// On the laptop, another guest plays, signs in with Telegram, and links Google there.
	laptop, laptopToken := guest()
	play(laptop.ID)
	out, err = s.SignIn(ctx, &laptop, "test", tg)
	if err != nil || out.Status != SignedIn || out.User.ID != phone.ID || out.Token != "" {
		t.Fatalf("laptop sign-in: %+v, %v", out, err)
	}
	if u, err := s.FromToken(ctx, laptopToken); err != nil || u.ID != phone.ID {
		t.Fatalf("the laptop's session is %v, %v; want the phone's account", u.ID, err)
	}
	if _, err := q.GetUser(ctx, laptop.ID); err == nil {
		t.Fatal("the laptop's guest is still there")
	}
	acct := out.User
	if out, err := s.SignIn(ctx, &acct, "test", google); err != nil || out.Status != Linked {
		t.Fatalf("link google: %+v, %v", out, err)
	}
	if n := games(acct.ID); n != 2 {
		t.Fatalf("history: %d games, want both", n)
	}
	if u, _ := s.FromToken(ctx, phoneToken); u.ID != acct.ID {
		t.Fatal("the phone lost its session")
	}

	// A second account signs in with that Telegram: offered a merge, says yes.
	other, _ := guest()
	play(other.ID)
	if _, err := s.SignIn(ctx, &other, "test", auth.Identity{Provider: "google", Subject: "g-2"}); err != nil {
		t.Fatal(err)
	}
	other, _ = q.GetUser(ctx, other.ID)
	out, err = s.SignIn(ctx, &other, "test", tg)
	if err != nil || out.Status != Merge || out.Other.ID != acct.ID || out.MergeToken == "" {
		t.Fatalf("merge offer: %+v, %v", out, err)
	}
	if _, err := s.AcceptMerge(ctx, acct, out.MergeToken); !errors.Is(err, ErrNotFound) {
		t.Fatal("someone else's merge offer was accepted")
	}
	merged, err := s.AcceptMerge(ctx, other, out.MergeToken)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := q.UserIdentities(ctx, merged.ID)
	if n := games(merged.ID); n != 3 || len(ids) != 3 {
		t.Fatalf("after merge: %d games, %d identities; want 3 and 3", n, len(ids))
	}
	if u, _ := s.FromToken(ctx, phoneToken); u.ID != merged.ID {
		t.Fatal("the merged account's phone is signed out")
	}

	// Unlinking: never the last way in.
	for _, id := range ids[:2] {
		if err := s.Unlink(ctx, merged, IdentityID(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Unlink(ctx, merged, IdentityID(ids[2])); !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("unlinked the last identity: %v", err)
	}

	// Names go through the filter; avatars come from the list.
	if _, err := s.UpdateProfile(ctx, merged, ptr("suka"), nil); !errors.Is(err, ErrBadName) {
		t.Fatal("a rude name passed")
	}
	if _, err := s.UpdateProfile(ctx, merged, nil, ptr("💩")); !errors.Is(err, ErrBadAvatar) {
		t.Fatal("an avatar off the list passed")
	}
	if u, err := s.UpdateProfile(ctx, merged, ptr(" Ali  Valiyev "), ptr("🦊")); err != nil || u.Name != "Ali Valiyev" || u.Avatar != "🦊" {
		t.Fatalf("profile: %+v, %v", u, err)
	}

	// Signing out everywhere ends every session.
	if err := s.Logout(ctx, &merged, phoneToken, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FromToken(ctx, laptopToken); !errors.Is(err, ErrNoSession) {
		t.Fatal("the laptop is still signed in")
	}
}

func ptr(s string) *string { return &s }
