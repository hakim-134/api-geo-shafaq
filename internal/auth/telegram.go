package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TelegramUser is who pressed Start in the bot, or who opened the Mini App.
type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u TelegramUser) Identity() Identity {
	username := ""
	if u.Username != "" {
		username = "@" + u.Username
	}
	return Identity{
		Provider: "telegram", Subject: strconv.FormatInt(u.ID, 10),
		Name: fullName(u.FirstName, u.LastName), Username: username,
	}
}

// WebAppMaxAge: a Mini App's initData older than this is not accepted.
const WebAppMaxAge = 24 * time.Hour

// TelegramWebApp checks a Mini App's initData (core.telegram.org/bots/webapps, "Validating
// data received via the Mini App"): the fields, sorted, joined by newlines, signed with
// HMAC-SHA256 under HMAC-SHA256("WebAppData", bot token).
func TelegramWebApp(initData, botToken string, now time.Time) (TelegramUser, error) {
	var u TelegramUser
	if botToken == "" {
		return u, ErrBadLogin
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return u, ErrBadLogin
	}
	got := vals.Get("hash")
	if got == "" {
		return u, ErrBadLogin
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(lines, "\n"))))
	if !hmac.Equal([]byte(want), []byte(got)) {
		return u, ErrBadLogin
	}
	at, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > WebAppMaxAge {
		return u, ErrBadLogin
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return u, fmt.Errorf("%w: no user", ErrBadLogin)
	}
	return u, nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// TelegramBotName asks Telegram for the bot's username, for the t.me/<bot> links.
func TelegramBotName(ctx context.Context, client *http.Client, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getMe", nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("telegram getMe: " + redact(err.Error(), token))
	}
	defer res.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || !out.OK || out.Result.Username == "" {
		return "", fmt.Errorf("telegram getMe: %s", res.Status)
	}
	return out.Result.Username, nil
}

// redact keeps the bot token out of error messages (it is in the URL).
func redact(s, token string) string { return strings.ReplaceAll(s, token, "<token>") }
