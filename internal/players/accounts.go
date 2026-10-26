package players

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// How a sign-in ended.
const (
	SignedIn = "signed_in" // this device is now that account; a guest's games moved into it
	Linked   = "linked"    // the identity was added to the current player
	Already  = "already"   // it was already this player's
	Merge    = "merge"     // it belongs to another account: ask the player to join the two
	Pending  = "pending"   // Telegram bot: Start not pressed yet
)

var (
	ErrNotFound     = errors.New("not_found")
	ErrLastIdentity = errors.New("last_identity")
	ErrBadAvatar    = errors.New("bad_input")
)

// Outcome of a sign-in.
type Outcome struct {
	Status string
	User   store.User // the player on this device now
	Token  string     // a new session for this device, when there was none
	// Merge: the other account and the token that says yes to joining it into this one.
	MergeToken string
	Other      store.User
}

const (
	TelegramCodeTTL = 10 * time.Minute
	mergeOfferTTL   = 10 * time.Minute
)

// SignIn attaches a verified identity to the player on this device (cur; nil if the device
// has no session yet):
//
//   - a new identity becomes a way into the current player; a guest becomes an account and
//     keeps everything;
//   - an identity of another account: a guest moves into that account (its games come
//     along, and this device is signed in to it); an account is offered a merge.
func (s *Service) SignIn(ctx context.Context, cur *store.User, userAgent string, id auth.Identity) (Outcome, error) {
	owner, err := s.Q.IdentityOwner(ctx, store.IdentityOwnerParams{Provider: id.Provider, Subject: id.Subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.attach(ctx, cur, userAgent, id)
	}
	if err != nil {
		return Outcome{}, err
	}
	if err := s.Q.TouchIdentity(ctx, store.TouchIdentityParams{
		Provider: id.Provider, Subject: id.Subject, Name: id.Name, Username: id.Username,
	}); err != nil {
		return Outcome{}, err
	}
	account, err := s.Q.GetUser(ctx, owner.UserID)
	if err != nil {
		return Outcome{}, err
	}
	switch {
	case cur != nil && cur.ID == account.ID:
		return Outcome{Status: Already, User: account}, nil
	case cur == nil:
		token, err := s.newSession(ctx, account, userAgent)
		return Outcome{Status: SignedIn, User: account, Token: token}, err
	case cur.Guest:
		// Nothing of the guest's is lost: its games and this device's session move in.
		if err := s.merge(ctx, account.ID, cur.ID); err != nil {
			return Outcome{}, err
		}
		account, err = s.Q.GetUser(ctx, account.ID)
		return Outcome{Status: SignedIn, User: account}, err
	default:
		token, err := randomToken()
		if err != nil {
			return Outcome{}, err
		}
		err = s.Q.CreateMergeOffer(ctx, store.CreateMergeOfferParams{
			TokenHash: hash(token), UserID: cur.ID, OtherID: account.ID, ExpiresAt: time.Now().Add(mergeOfferTTL),
		})
		return Outcome{Status: Merge, User: *cur, MergeToken: token, Other: account}, err
	}
}

func (s *Service) attach(ctx context.Context, cur *store.User, userAgent string, id auth.Identity) (Outcome, error) {
	out := Outcome{Status: Linked}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := s.Q.WithTx(tx)
		if cur == nil {
			avatar := Avatars[randInt(len(Avatars))]
			u, err := q.CreateUser(ctx, store.CreateUserParams{Name: GuestName(avatar, randInt(100)), Avatar: avatar})
			if err != nil {
				return err
			}
			cur, out.Status = &u, SignedIn
		}
		if err := q.AddIdentity(ctx, store.AddIdentityParams{
			Provider: id.Provider, Subject: id.Subject, UserID: cur.ID, Name: id.Name, Username: id.Username,
		}); err != nil {
			return err
		}
		if err := q.SetAccount(ctx, cur.ID); err != nil {
			return err
		}
		u, err := q.GetUser(ctx, cur.ID)
		out.User = u
		return err
	})
	if err == nil && out.Status == SignedIn {
		out.Token, err = s.newSession(ctx, out.User, userAgent)
	}
	return out, err
}

// AcceptMerge joins the other account of a merge offer into the current one.
func (s *Service) AcceptMerge(ctx context.Context, cur store.User, token string) (store.User, error) {
	offer, err := s.Q.TakeMergeOffer(ctx, store.TakeMergeOfferParams{TokenHash: hash(token), UserID: cur.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return cur, ErrNotFound
	}
	if err != nil {
		return cur, err
	}
	if err := s.merge(ctx, cur.ID, offer.OtherID); err != nil {
		return cur, err
	}
	return s.Q.GetUser(ctx, cur.ID)
}

// merge moves everything of other into keep, then deletes other. Its devices stay signed
// in, to keep.
func (s *Service) merge(ctx context.Context, keep, other uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := s.Q.WithTx(tx)
		err := errors.Join(
			q.MergeGames(ctx, store.MergeGamesParams{UserID: keep, OtherID: other}),
			q.MergeGuesses(ctx, store.MergeGuessesParams{UserID: keep, OtherID: other}),
			q.MergeIdentities(ctx, store.MergeIdentitiesParams{UserID: keep, OtherID: other}),
			q.MergeSessions(ctx, store.MergeSessionsParams{UserID: keep, OtherID: other}),
			q.MergeMaps(ctx, store.MergeMapsParams{UserID: &keep, OtherID: &other}),
			q.MergeBan(ctx, store.MergeBanParams{UserID: keep, OtherID: other}),
		)
		if err != nil {
			return err
		}
		if err := q.SetAccount(ctx, keep); err != nil {
			return err
		}
		return q.DeleteUser(ctx, other)
	})
}

// IdentityID is the opaque id the game uses to unlink an identity.
func IdentityID(i store.Identity) string { return i.Provider + ":" + i.Subject }

// Unlink removes a way to sign in; never the last one.
func (s *Service) Unlink(ctx context.Context, cur store.User, id string) error {
	provider, subject, ok := strings.Cut(id, ":")
	if !ok {
		return ErrNotFound
	}
	n, err := s.Q.DeleteIdentity(ctx, store.DeleteIdentityParams{Provider: provider, Subject: subject, UserID: cur.ID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLastIdentity
	}
	return nil
}

// Logout ends this device's session, or every session of the player.
func (s *Service) Logout(ctx context.Context, u *store.User, token string, everywhere bool) error {
	if everywhere && u != nil {
		_, err := s.Q.DeleteUserSessions(ctx, u.ID)
		return err
	}
	return s.Q.DeleteSession(ctx, hash(token))
}

// UpdateProfile changes the name (checked by the name filter) and the avatar (from the list).
func (s *Service) UpdateProfile(ctx context.Context, cur store.User, name, avatar *string) (store.User, error) {
	p := store.SetProfileParams{ID: cur.ID, Name: cur.Name, Avatar: cur.Avatar}
	if name != nil {
		n, err := CleanName(*name)
		if err != nil {
			return cur, err
		}
		p.Name = n
	}
	if avatar != nil {
		if !slices.Contains(Avatars, *avatar) {
			return cur, ErrBadAvatar
		}
		p.Avatar = *avatar
	}
	return s.Q.SetProfile(ctx, p)
}

// StartTelegram makes a one-time code for "sign in with Telegram" through the bot: the game
// opens t.me/<bot>?start=login_<code>, the bot confirms it (ConfirmTelegram), the game
// collects it (CheckTelegram).
func (s *Service) StartTelegram(ctx context.Context, cur store.User) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	err = s.Q.CreateTelegramLogin(ctx, store.CreateTelegramLoginParams{
		CodeHash: hash(code), UserID: cur.ID, ExpiresAt: time.Now().Add(TelegramCodeTTL),
	})
	return code, err
}

// ConfirmTelegram is the bot's side: who pressed Start for a code. False if the code is
// unknown, used or too old.
func (s *Service) ConfirmTelegram(ctx context.Context, code string, u auth.TelegramUser) (bool, error) {
	if code == "" || len(code) > 64 {
		return false, nil
	}
	id := u.Identity()
	n, err := s.Q.ConfirmTelegramLogin(ctx, store.ConfirmTelegramLoginParams{
		CodeHash: hash(code), TgID: &u.ID, TgName: id.Name, TgUsername: id.Username,
	})
	return n > 0, err
}

// CheckTelegram is the game's side: Pending until the bot confirmed the code, then the
// sign-in. ErrNotFound for a code that is not this player's or has expired.
func (s *Service) CheckTelegram(ctx context.Context, cur store.User, userAgent, code string) (Outcome, error) {
	if code == "" || len(code) > 64 {
		return Outcome{}, ErrNotFound
	}
	h := hash(code)
	row, err := s.Q.TakeTelegramLogin(ctx, store.TakeTelegramLoginParams{CodeHash: h, UserID: cur.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		waiting, err := s.Q.TelegramLoginWaiting(ctx, store.TelegramLoginWaitingParams{CodeHash: h, UserID: cur.ID})
		if err != nil {
			return Outcome{}, err
		}
		if !waiting {
			return Outcome{}, ErrNotFound
		}
		return Outcome{Status: Pending, User: cur}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if row.TgID == nil {
		return Outcome{}, ErrNotFound
	}
	tu := auth.TelegramUser{ID: *row.TgID}
	id := tu.Identity()
	id.Name, id.Username = row.TgName, row.TgUsername
	return s.SignIn(ctx, &cur, userAgent, id)
}

func randomToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
