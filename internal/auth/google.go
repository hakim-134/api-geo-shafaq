package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Google checks the ID tokens of Google's sign-in button (Google Identity Services): an
// RS256 JWT signed with one of Google's published keys, for our client id. Free: no Maps
// billing is involved.
type Google struct {
	ClientID string
	CertsURL string // "" = Google's; tests point it at a fake
	Client   *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

const googleCerts = "https://www.googleapis.com/oauth2/v3/certs"

type googleClaims struct {
	Iss           string `json:"iss"`
	Aud           string `json:"aud"`
	Sub           string `json:"sub"`
	Exp           int64  `json:"exp"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// Verify returns the Google account of an ID token.
func (g *Google) Verify(ctx context.Context, token string, now time.Time) (Identity, error) {
	if g.ClientID == "" {
		return Identity{}, ErrBadLogin
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrBadLogin
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil || head.Alg != "RS256" {
		return Identity{}, ErrBadLogin
	}
	key, err := g.key(ctx, head.Kid)
	if err != nil {
		return Identity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, ErrBadLogin
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return Identity{}, ErrBadLogin
	}
	var c googleClaims
	if err := decodePart(parts[1], &c); err != nil {
		return Identity{}, ErrBadLogin
	}
	switch {
	case c.Iss != "accounts.google.com" && c.Iss != "https://accounts.google.com",
		c.Aud != g.ClientID, c.Sub == "", now.Unix() > c.Exp+60:
		return Identity{}, ErrBadLogin
	}
	email := ""
	if c.EmailVerified {
		email = c.Email
	}
	return Identity{Provider: "google", Subject: c.Sub, Name: c.Name, Username: email}, nil
}

// key finds a signing key by id, fetching Google's list at most every 5 minutes (and again
// at once for a key it has not seen: Google rotates them).
func (g *Google) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if k, ok := g.keys[kid]; ok {
		return k, nil
	}
	if time.Since(g.fetched) < 5*time.Minute && g.keys != nil {
		return nil, ErrBadLogin
	}
	keys, err := g.fetch(ctx)
	if err != nil {
		return nil, err
	}
	g.keys, g.fetched = keys, time.Now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, ErrBadLogin
}

func (g *Google) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	u := g.CertsURL
	if u == "" {
		u = googleCerts
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google certs: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google certs: %s", res.Status)
	}
	var set struct {
		Keys []struct {
			Kid, Kty, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("google certs: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, nil
}

func decodePart(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
