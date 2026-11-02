package card

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestDraw(t *testing.T) {
	for _, d := range []Data{
		{Name: "Sabzi 17", Sign: Sign{Kind: "blue", Top: "Asosiy ko‘chalar", Name: "Toshkent ko‘chalari"}, Total: 12780, Scores: []int{4800, 2600, 100, 5000, 3000}},
		{Name: "🦉 " + strings.Repeat("Juda uzun ism ", 8), Sign: Sign{Kind: "white", Top: "Toshkent", Name: strings.Repeat("Mirzo Ulug‘bek tumani ", 4)}, Total: 0},
	} {
		b, err := Draw(d)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if s := img.Bounds().Size(); s.X != Width || s.Y != Height {
			t.Fatalf("size %v", s)
		}
	}
}

func TestLongNamesAreCutAndEmojiLeftOut(t *testing.T) {
	l := fitted(body, 46, 600, "🦉 "+strings.Repeat("Juda uzun ism ", 8))
	if strings.Contains(l.text, "🦉") || !strings.HasSuffix(l.text, "…") || width(body, l.size, l.text) > 600 {
		t.Fatalf("%+v", l)
	}
	if Group(12780) != "12 780" || Group(980) != "980" {
		t.Fatal(Group(12780))
	}
}
