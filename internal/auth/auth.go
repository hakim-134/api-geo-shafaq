// Package auth checks that a sign-in really comes from Telegram or Google. It knows nothing
// of our users: it turns a provider's signed proof into an Identity, and package players
// attaches that to a player.
package auth

import (
	"errors"
	"strings"
)

// Identity is a verified account at a provider.
type Identity struct {
	Provider string // telegram | google
	Subject  string // the provider's stable user id
	Name     string // shown on the profile so the player knows which account it is
	Username string // @username (Telegram) or the e-mail address (Google)
}

// ErrBadLogin: the proof is missing, forged, for someone else or too old.
var ErrBadLogin = errors.New("bad_login")

func fullName(first, last string) string {
	return strings.TrimSpace(first + " " + last)
}
