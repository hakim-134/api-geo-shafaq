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
