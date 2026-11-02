// Package card draws the picture of a challenge's link preview: what a Telegram group sees before
// anyone taps. The game's own look, in Uzbek first: night blue, the flag's stripe, the map's road
// sign, the score to beat and its row of squares.
package card

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The game's fonts (SIL Open Font License, in fonts/), cut to one weight and the alphabets used.
var (
	//go:embed fonts/Unbounded-ExtraBold.ttf
	displayTTF []byte
	//go:embed fonts/Onest-Bold.ttf
	bodyTTF []byte

	display = mustParse(displayTTF)
	body    = mustParse(bodyTTF)
)

func mustParse(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic(err)
	}
	return f
}

const Width, Height = 1200, 630

var (
	night  = color.RGBA{0x13, 0x23, 0x3f, 255}
	white  = color.RGBA{255, 255, 255, 255}
	muted  = color.RGBA{0xb8, 0xc4, 0xd8, 255}
	sky    = color.RGBA{0x2b, 0xb7, 0xd3, 255}
	gold   = color.RGBA{0xf5, 0xb8, 0x1c, 255}
	green  = color.RGBA{0x1e, 0xb5, 0x3a, 255}
	greenE = color.RGBA{0x12, 0x7f, 0x27, 255}
	red    = color.RGBA{0xce, 0x11, 0x26, 255}
	flagB  = color.RGBA{0x00, 0x99, 0xb5, 255}
)

// Sign is a map's road sign: its plate's colour, and white or dark letters on it.
type Sign struct {
	Kind string // blue | white | brown | green, as the game's sign.ts
	Top  string // the small line, the larger place
	Name string
}

// Data is what a card says. Name is the challenger; Scores their rounds, one square each.
type Data struct {
	Name   string
	Sign   Sign
	Total  int
	Scores []int
}

// Draw makes the card as a PNG.
func Draw(d Data) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), &image.Uniform{night}, image.Point{}, draw.Src)

	// the flag's stripe, as on top of every page
	stripe := []struct {
		c color.Color
		h int
	}{{flagB, 12}, {red, 2}, {white, 4}, {red, 2}, {green, 12}}
	y := 0
	for _, s := range stripe {
		draw.Draw(img, image.Rect(0, y, Width, y+s.h), &image.Uniform{s.c}, image.Point{}, draw.Src)
		y += s.h
	}

	const left = 72
	x := text(img, display, 38, left, 104, "Shafaq", white)
	text(img, display, 38, x, 104, "Geo", sky)

	line := fitted(body, 46, Width-2*left, d.Name+" sizni chaqirdi")
	text(img, body, line.size, left, 186, line.text, white)

	plate(img, left, 216, Width-2*left, d.Sign)

	// the score to beat, big, and its squares
	score := Group(d.Total)
	x = text(img, display, 104, left, 516, score, gold)
	text(img, body, 44, x+20, 516, "ball", muted)
	squares(img, left, 548, d.Scores)

	button(img, Width-left-320, 452, 320, 104)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Group writes 12780 as "12 780", as the game does.
func Group(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	return s[:len(s)-3] + " " + s[len(s)-3:]
}

// plate is the map's road sign: a colour, a white inner line, two bolts, the name big.
func plate(img *image.RGBA, x, y, maxW int, s Sign) {
	bg, ink := color.RGBA{0x1d, 0x4f, 0x9a, 255}, color.Color(white)
	rim := color.Color(white)
	switch s.Kind {
	case "white":
		bg, ink, rim = color.RGBA{0xfb, 0xfa, 0xf6, 255}, night, night
	case "brown":
		bg = color.RGBA{0x7b, 0x4a, 0x2b, 255}
	case "green":
		bg = color.RGBA{0x17, 0x78, 0x4a, 255}
	}
	const padX = 44
	name := fitted(display, 44, maxW-2*padX, s.Name)
	top := fitted(body, 22, maxW-2*padX, s.Top)
	w := max(width(display, name.size, name.text), width(body, top.size, top.text)) + 2*padX
	const h = 136
	round(img, x, y, w, h, 18, bg)
	frame(img, x+8, y+8, w-16, h-16, 12, 3, rim, bg)
	round(img, x+15, y+h/2-4, 8, 8, 4, rim)
	round(img, x+w-23, y+h/2-4, 8, 8, 4, rim)
	text(img, body, top.size, x+padX, y+50, top.text, ink)
	text(img, display, name.size, x+padX, y+106, name.text, ink)
}

// squares: one a round, as the game's share text has them (🟩🟨🟥).
func squares(img *image.RGBA, x, y int, scores []int) {
	const size, gap = 38, 10
	if len(scores) > 12 {
		scores = scores[:12]
	}
	for i, sc := range scores {
		c := red
		switch {
		case sc >= 4000:
			c = green
		case sc >= 2500:
			c = gold
		}
		round(img, x+i*(size+gap), y, size, size, 8, c)
	}
}

// button is "O‘ynash ▶", as the game's own green plate with its darker edge.
func button(img *image.RGBA, x, y, w, h int) {
	round(img, x, y+8, w, h, 26, greenE)
	round(img, x, y, w, h, 26, green)
	label := "O‘ynash"
	tw := width(display, 36, label)
	sx := x + (w-tw-44)/2
	end := text(img, display, 36, sx, y+h/2+14, label, white)
	triangle(img, end+18, y+h/2-14, 24, 28, white)
}

type line struct {
	text string
	size float64
}

// fitted shrinks a line to fit, then cuts it: a long street or a long name stays on the card.
func fitted(f *opentype.Font, size float64, maxW int, s string) line {
	s = known(f, s)
	for sz := size; sz >= size*0.6; sz -= 2 {
		if width(f, sz, s) <= maxW {
			return line{s, sz}
		}
	}
	small := size * 0.6
	for width(f, small, s+"…") > maxW && utf8.RuneCountInString(s) > 1 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return line{strings.TrimRight(s, " ") + "…", small}
}

// known leaves out what the font can't draw (a chosen name may hold an emoji).
func known(f *opentype.Font, s string) string {
	face := newFace(f, 20)
	defer face.Close()
	out := strings.Map(func(r rune) rune {
		if _, ok := face.GlyphAdvance(r); ok {
			return r
		}
		return -1
	}, s)
	return strings.TrimSpace(out)
}

func newFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err) // only a size below zero does this
	}
	return face
}

func width(f *opentype.Font, size float64, s string) int {
	face := newFace(f, size)
	defer face.Close()
	return font.MeasureString(face, s).Ceil()
}

// text draws s with its baseline at y and says where it ended.
func text(img *image.RGBA, f *opentype.Font, size float64, x, y int, s string, c color.Color) int {
	face := newFace(f, size)
	defer face.Close()
	d := font.Drawer{Dst: img, Src: &image.Uniform{c}, Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Ceil()
}

// round fills a rounded rectangle, its edges smooth.
func round(img *image.RGBA, x, y, w, h, r int, c color.Color) {
	ras := vector.NewRasterizer(w, h)
	path(ras, 0, 0, float32(w), float32(h), float32(r))
	ras.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{c}, image.Point{})
}

// frame is a rounded line: a rounded rectangle with the inside filled back.
func frame(img *image.RGBA, x, y, w, h, r, t int, c, inside color.Color) {
	round(img, x, y, w, h, r, c)
	round(img, x+t, y+t, w-2*t, h-2*t, r-t, inside)
}

func path(ras *vector.Rasterizer, x, y, w, h, r float32) {
	const k = 0.5523 // a quarter circle as a curve
	c := r * (1 - k)
	ras.MoveTo(x+r, y)
	ras.LineTo(x+w-r, y)
	ras.CubeTo(x+w-c, y, x+w, y+c, x+w, y+r)
	ras.LineTo(x+w, y+h-r)
	ras.CubeTo(x+w, y+h-c, x+w-c, y+h, x+w-r, y+h)
	ras.LineTo(x+r, y+h)
	ras.CubeTo(x+c, y+h, x, y+h-c, x, y+h-r)
	ras.LineTo(x, y+r)
	ras.CubeTo(x, y+c, x+c, y, x+r, y)
	ras.ClosePath()
}

func triangle(img *image.RGBA, x, y, w, h int, c color.Color) {
	ras := vector.NewRasterizer(w, h)
	ras.MoveTo(0, 0)
	ras.LineTo(float32(w), float32(h)/2)
	ras.LineTo(0, float32(h))
	ras.ClosePath()
	ras.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{c}, image.Point{})
}
