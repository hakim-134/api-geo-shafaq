// Package players is guests, accounts and sessions. Everyone is a player from the first
// visit: a guest with an emoji and a name like "Panda 42", no sign-in wall. Ways to sign in (Telegram,
// Google) attach to the same user later, so nothing is lost (accounts.go).
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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// avatars are the emojis guests get, each with its word: animals, plants and friendly things
// that look fine on a big screen in class or at work. Reviewed by hand; nothing rude, sad,
// national or gambling-like (no 🍑, no 🎲), and none whose word is a tease (🦥 "yalqov",
// lazy; 🐵 "maymun", said of people too). The word is the guest's name with two digits
// ("Ukki 42"): Uzbek, the same in every language, so a friend reads out the name they see.
var avatars = [][2]string{
	{"🐼", "Panda"}, {"🦊", "Tulki"}, {"🐨", "Koala"}, {"🐯", "Yo‘lbars"}, {"🦁", "Sher"},
	{"🐸", "Qurbaqa"}, {"🦉", "Ukki"}, {"🐧", "Pingvin"}, {"🐢", "Toshbaqa"},
	{"🐙", "Sakkizoyoq"}, {"🦋", "Kapalak"}, {"🐝", "Asalari"}, {"🐞", "Xonqizi"}, {"🦄", "Yakkashox"},
	{"🐳", "Kit"}, {"🐬", "Delfin"}, {"🦒", "Jirafa"}, {"🦓", "Zebra"}, {"🐘", "Fil"},
	{"🦔", "Tipratikan"}, {"🦦", "Suvsar"}, {"🐿️", "Olmaxon"}, {"🦩", "Flamingo"},
	{"🦜", "To‘tiqush"}, {"🐰", "Quyon"}, {"🐻", "Ayiq"}, {"🐻‍❄️", "Oqayiq"}, {"🐹", "Xomyak"},
	{"🐱", "Mushuk"}, {"🐶", "Kuchukcha"}, {"🐴", "Ot"}, {"🐫", "Tuya"}, {"🦌", "Bug‘u"},
	{"🦚", "Tovus"}, {"🦭", "Tyulen"}, {"🐠", "Baliqcha"}, {"🦀", "Qisqichbaqa"}, {"🐌", "Chig‘anoq"},
	{"🌵", "Kaktus"}, {"🌻", "Kungaboqar"}, {"🌷", "Lola"}, {"🍀", "Beda"}, {"🌳", "Chinor"},
	{"🍉", "Tarvuz"}, {"🍎", "Olma"}, {"🍇", "Uzum"}, {"🍋", "Limon"}, {"🥝", "Kivi"},
	{"🥕", "Sabzi"}, {"🌽", "Makkajo‘xori"}, {"🍄", "Qo‘ziqorin"}, {"⭐", "Yulduz"}, {"🌙", "Oy"},
	{"⛰️", "Tog‘"}, {"🎈", "Shar"}, {"🪁", "Varrak"}, {"🚲", "Velosiped"}, {"🚀", "Raketa"},
	{"⚽", "To‘p"}, {"🏀", "Basketbol"}, {"🎨", "Rassom"}, {"🧩", "Jumboq"}, {"📚", "Kitob"},
	{"🎧", "Quloqchin"}, {"🧭", "Kompas"}, {"🗺️", "Xarita"},
}

// Avatars are the emojis a player may pick.
var Avatars = func() []string {
	out := make([]string, len(avatars))
	for i, a := range avatars {
		out[i] = a[0]
	}
	return out
}()

var avatarWords = func() map[string]string {
	out := make(map[string]string, len(avatars))
	for _, a := range avatars {
		out[a[0]] = a[1]
	}
	return out
}()

// GuestName is the avatar's word and a number from 10 to 99: "Ukki 42" (P1028 2.4; until
// 2026-10-29 "🦉_42", which read like a password). Names are not unique; the user id tells
// players apart. 69 is skipped: a family's child plays this too.
func GuestName(avatar string, n int) string {
	if n %= 100; n < 10 {
		n += 10
	}
	if n == 69 {
		n = 96
	}
	word := avatarWords[avatar]
	if word == "" {
		word = "Mehmon"
	}
	return fmt.Sprintf("%s %d", word, n)
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
	Pool *pgxpool.Pool
	Q    *store.Queries
	TTL  time.Duration
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
	u := store.User{
		ID: row.ID, Name: row.Name, Avatar: row.Avatar, Guest: row.Guest, CreatedAt: row.CreatedAt,
		LastSeenAt: row.LastSeenAt, BannedAt: row.BannedAt, BanReason: row.BanReason, Prefs: row.Prefs,
	}
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
