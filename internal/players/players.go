// Package players is guests and sessions. Everyone is a player from the first visit: a
// guest with an emoji name like 🐼_42, no sign-in wall. Accounts (Google, Telegram) attach
// to the same user later, so nothing is lost.
package players

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Avatars are the emojis guests get: animals, plants and friendly things that look fine
// on a big screen in class or at work. Reviewed by hand; nothing rude, sad, national or
// gambling-like (no 🍑, no 🎲).
var Avatars = []string{
	"🐼", "🦊", "🐨", "🐯", "🦁", "🐸", "🐵", "🦉", "🐧", "🐢",
	"🐙", "🦋", "🐝", "🐞", "🦄", "🐳", "🐬", "🦒", "🦓", "🐘",
	"🦔", "🦦", "🦥", "🐿️", "🦩", "🦜", "🐰", "🐻", "🐻‍❄️", "🐹",
	"🐱", "🐶", "🐴", "🐫", "🦌", "🦚", "🦭", "🐠", "🦀", "🐌",
	"🌵", "🌻", "🌷", "🍀", "🌳", "🍉", "🍎", "🍇", "🍋", "🥝",
	"🥕", "🌽", "🍄", "⭐", "🌙", "⛰️", "🎈", "🪁", "🚲", "🚀",
	"⚽", "🏀", "🎨", "🧩", "📚", "🎧", "🧭", "🗺️",
}

// GuestName is the emoji and two digits: 🐼_42. Names are not unique (68 × 100); the user
// id tells players apart.
func GuestName(avatar string, n int) string {
	return fmt.Sprintf("%s_%02d", avatar, n%100)
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err) // the system's random source is broken; nothing else is safe either
	}
	return int(v.Int64())
}

var ErrNoSession = errors.New("no session")

type Service struct {
	Q   *store.Queries
	TTL time.Duration
}

// NewGuest creates a guest and a session for it. The token goes into the cookie; only its
// hash is stored.
func (s *Service) NewGuest(ctx context.Context, userAgent string) (store.User, string, error) {
	avatar := Avatars[randInt(len(Avatars))]
	u, err := s.Q.CreateUser(ctx, store.CreateUserParams{Name: GuestName(avatar, randInt(100)), Avatar: avatar})
	if err != nil {
		return u, "", err
	}
	token, err := s.newSession(ctx, u, userAgent)
	if err == nil {
		metrics.GuestsCreated.Inc()
	}
	return u, token, err
}

func (s *Service) newSession(ctx context.Context, u store.User, userAgent string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	err := s.Q.CreateSession(ctx, store.CreateSessionParams{
		TokenHash: hash(token), UserID: u.ID, UserAgent: userAgent, ExpiresAt: time.Now().Add(s.TTL),
	})
	return token, err
}

// FromToken returns the player of a session token. A used session slides its expiry, at
// most once an hour so a game does not write on every request.
func (s *Service) FromToken(ctx context.Context, token string) (store.User, error) {
	if token == "" || len(token) > 100 {
		return store.User{}, ErrNoSession
	}
	h := hash(token)
	row, err := s.Q.SessionUser(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.User{}, ErrNoSession
	}
	if err != nil {
		return store.User{}, err
	}
	u := store.User{ID: row.ID, Name: row.Name, Avatar: row.Avatar, Guest: row.Guest, CreatedAt: row.CreatedAt, LastSeenAt: row.LastSeenAt}
	if time.Since(row.SessionUsedAt) > time.Hour {
		if err := s.Q.TouchSession(ctx, store.TouchSessionParams{TokenHash: h, ExpiresAt: time.Now().Add(s.TTL)}); err != nil {
			return u, err
		}
		if err := s.Q.TouchUser(ctx, u.ID); err != nil {
			return u, err
		}
	}
	return u, nil
}

// Stats are the numbers on the profile card.
type Stats struct{ Games, Best, AvgRound int }

func (s *Service) Stats(ctx context.Context, u store.User) (Stats, error) {
	r, err := s.Q.UserStats(ctx, u.ID)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Games: int(r.Games), Best: int(r.Best)}
	if r.RoundsSum > 0 {
		st.AvgRound = int(r.ScoreSum / r.RoundsSum)
	}
	return st, nil
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
