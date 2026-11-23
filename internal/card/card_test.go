package card

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

// a square place of two parts with a lake (a hole) in the left one; the right one is the place
var twoParts = []Shape{
	{D: "M 0 0 L 0 100 50 100 50 0 Z M 10 10 L 40 10 40 40 10 40 Z"},
	{D: "M 50 0 L 50 100 100 100 100 0 Z", Lit: true},
}

func TestDraw(t *testing.T) {
	for _, d := range []Data{
		{Name: "Sabzi 17", Kind: "Asosiy ko‘chalar", Map: "Toshkent ko‘chalari", Face: twoParts, Total: 12780, Scores: []int{4800, 2600, 100, 5000, 3000}},
		{Name: "🦉 " + strings.Repeat("Juda uzun ism ", 8), Kind: "Toshkent", Map: strings.Repeat("Mirzo Ulug‘bek tumani ", 4), Total: 0},
		{Name: "Азиз", Kind: "Бухарская область", Map: "Жондор", Face: twoParts[1:], Total: 980, Scores: []int{980}},
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

func TestTheFaceFillsThePlaceGoldAndKeepsItsHoles(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 220, 220))
	face(img, twoParts, 10, 10, 200)
	at := func(x, y int) [3]uint8 {
		c := img.RGBAAt(x, y)
		return [3]uint8{c.R, c.G, c.B}
	}
	isGold := func(c [3]uint8) bool { return c[0] > 220 && c[1] > 160 && c[1] < 230 && c[2] < 120 }
	isMilk := func(c [3]uint8) bool { return c[0] > 230 && c[1] > 220 && c[2] > 190 }
	if !isGold(at(160, 110)) {
		t.Errorf("the place isn't gold: %v", at(160, 110))
	}
	if !isMilk(at(30, 160)) {
		t.Errorf("the other part isn't milk: %v", at(30, 160))
	}
	if c := at(60, 60); isMilk(c) || isGold(c) {
		t.Errorf("the lake was filled: %v", c)
	}
}

func TestPathsAsTheServerDrawsThem(t *testing.T) {
	rs := parse("M 720.7 654.5 L 719.2 653.8 716.9 657.7 Z M 1 2 L 3 4 5 6 7 8 Z")
	if len(rs) != 2 || len(rs[0]) != 3 || rs[1][3] != (pt{7, 8}) {
		t.Fatalf("%v", rs)
	}
}

func TestLongNamesAreCutAndEmojiLeftOut(t *testing.T) {
	l := fitted(body, 46, 600, "🦉 "+strings.Repeat("Juda uzun ism ", 8))
	if strings.Contains(l.text, "🦉") || !strings.HasSuffix(l.text, "...") || width(body, l.size, l.text) > 600 {
		t.Fatalf("%+v", l)
	}
	if l := fitted(body, 40, 900, "Azim 🦉 sizni chaqirdi"); l.text != "Azim sizni chaqirdi" {
		t.Fatalf("%q", l.text)
	}
	if Group(12780) != "12 780" || Group(980) != "980" {
		t.Fatal(Group(12780))
	}
}
