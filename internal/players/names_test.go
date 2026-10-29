package players

import (
	"errors"
	"testing"
)

func TestCleanName(t *testing.T) {
	ok := map[string]string{
		"  Ali   Valiyev ": "Ali Valiyev",
		"Nazira":           "Nazira",
		"Amanda":           "Amanda",
		"Spiderman":        "Spiderman",
		"Grape_77":         "Grape_77",
		"Hammangiz":        "Hammangiz",
		"Abdulla":          "Abdulla",
		"Allohberdi":       "Allohberdi",
		"Ko‘tarinki":       "Ko‘tarinki",
		"Musiqachi":        "Musiqachi",
		"Дилноза":          "Дилноза",
		"🐼 Panda":          "🐼 Panda",
		"G‘ayrat-2":        "G‘ayrat-2",
	}
	for in, want := range ok {
		got, err := CleanName(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"a", "", "                ", "this name is far too long for a game",
		"fuck", "FuCk_you", "f.u.c.k", "Хуй", "xuyovich", "сука", "5uk4", "s u k a",
		"jalab", "Dalbayob", "admin", "Admin_01", "ShafaqGeo", "<script>", "a\x00b",
		"sik", "Nazi", "Alloh", "🐼🐼", "___",
	}
	for _, in := range bad {
		if got, err := CleanName(in); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: passed as %q", in, got)
		}
	}
}

func TestGuestNames(t *testing.T) {
	if got := GuestName("🦉", 42); got != "Ukki 42" {
		t.Errorf("🦉 42: %q", got)
	}
	if got := GuestName("🦉", 7); got != "Ukki 17" {
		t.Errorf("🦉 7: %q", got)
	}
	if got := GuestName("🦉", 69); got != "Ukki 96" {
		t.Errorf("🦉 69: %q", got)
	}
	// every guest's name passes the filter players' own names go through
	for _, a := range Avatars {
		name := GuestName(a, 42)
		if avatarWords[a] == "" {
			t.Errorf("%s has no word", a)
		}
		if got, err := CleanName(name); err != nil || got != name {
			t.Errorf("%s: %q does not pass the name filter: %v", a, name, err)
		}
	}
}
