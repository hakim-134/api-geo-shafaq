package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"
)

func TestTelegramWebApp(t *testing.T) {
	const token = "123:abc"
	now := time.Unix(1_800_000_000, 0)
	sign := func(fields map[string]string, botToken string) string {
		v := url.Values{}
		keys := []string{}
		for k, val := range fields {
			v.Set(k, val)
			keys = append(keys, k)
		}
		lines := ""
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				lines += "\n"
			}
			lines += k + "=" + fields[k]
		}
		secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
		v.Set("hash", hex.EncodeToString(hmacSHA256(secret, []byte(lines))))
		return v.Encode()
	}
	fields := map[string]string{
		"auth_date": fmt.Sprint(now.Add(-time.Hour).Unix()),
		"query_id":  "AAH",
		"user":      `{"id":42,"first_name":"Ali","last_name":"Valiyev","username":"ali"}`,
	}

	u, err := TelegramWebApp(sign(fields, token), token, now)
	if err != nil {
		t.Fatal(err)
	}
	id := u.Identity()
	if id.Subject != "42" || id.Name != "Ali Valiyev" || id.Username != "@ali" {
		t.Fatalf("identity: %+v", id)
	}

	if _, err := TelegramWebApp(sign(fields, "other:bot"), token, now); !errors.Is(err, ErrBadLogin) {
		t.Fatal("another bot's data passed")
	}
	tampered := sign(fields, token) + "&extra=1"
	if _, err := TelegramWebApp(tampered, token, now); !errors.Is(err, ErrBadLogin) {
		t.Fatal("tampered data passed")
	}
	old := map[string]string{"auth_date": fmt.Sprint(now.Add(-48 * time.Hour).Unix()), "user": fields["user"]}
	if _, err := TelegramWebApp(sign(old, token), token, now); !errors.Is(err, ErrBadLogin) {
		t.Fatal("old data passed")
	}
}

func TestGoogle(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer certs.Close()
	now := time.Unix(1_800_000_000, 0)
	jwt := func(kid string, claims map[string]any) string {
		enc := func(v any) string {
			b, _ := json.Marshal(v)
			return base64.RawURLEncoding.EncodeToString(b)
		}
		body := enc(map[string]string{"alg": "RS256", "kid": kid}) + "." + enc(claims)
		sum := sha256.Sum256([]byte(body))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		return body + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	claims := func(aud string, exp time.Time) map[string]any {
		return map[string]any{
			"iss": "https://accounts.google.com", "aud": aud, "sub": "1001", "exp": exp.Unix(),
			"email": "ali@example.com", "email_verified": true, "name": "Ali",
		}
	}
	g := &Google{ClientID: "client-1", CertsURL: certs.URL}
	ctx := context.Background()

	id, err := g.Verify(ctx, jwt("k1", claims("client-1", now.Add(time.Hour))), now)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "1001" || id.Username != "ali@example.com" || id.Name != "Ali" {
		t.Fatalf("identity: %+v", id)
	}
	for name, tok := range map[string]string{
		"other client": jwt("k1", claims("client-2", now.Add(time.Hour))),
		"expired":      jwt("k1", claims("client-1", now.Add(-time.Hour))),
		"unknown key":  jwt("k2", claims("client-1", now.Add(time.Hour))),
		"garbage":      "a.b.c",
	} {
		if _, err := g.Verify(ctx, tok, now); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}
