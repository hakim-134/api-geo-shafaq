// Package card draws the picture of a challenge's link preview: what a Telegram group sees before
// anyone taps. The game's own look (decision 0024 in the brief), in Uzbek first: Khiva's turquoise
// ground, the gold star and GeoShafaq, the map's face (its place inside its parent, gold among milk
// tiles) beside its name, the score to beat with its row of squares, and the one green key.
package card

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The game's fonts (SIL Open Font License, in fonts/), cut to one weight and the alphabets used:
// M PLUS Rounded 1c Black for the name, the map, the score and the key; Onest for text.
var (
	//go:embed fonts/MPLUSRounded1c-Black.ttf
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

// The new look's tokens by day (geo-shafaq/src/app.css).
var (
	groundTop = rgb(0x0d808d)
	groundBot = rgb(0x08606c)
	glow      = color.NRGBA{70, 215, 220, 97} // rgba(70, 215, 220, 0.38)
	white     = rgb(0xffffff)
	onGround3 = rgb(0xd6eeee)
	goldText  = rgb(0xffd45e)
	stack     = rgb(0x064e58)              // the stacked shadow titles float on
	stack2    = color.NRGBA{3, 40, 48, 89} // rgba(3, 40, 48, 0.35)
	gold      = rgb(0xf4b92e)
	goldHi    = rgb(0xffe08a)
	goldLo    = rgb(0xc98a0c)
	kickInk   = rgb(0x3a2600)
	starIn    = color.NRGBA{0x0a, 0x47, 0x53, 235}
	tileA     = rgb(0xfffdf4) // the map's parts: milk
	tileB     = rgb(0xf4e8cc)
	seam      = color.NRGBA{10, 96, 108, 150} // rgba(10, 96, 108, 0.59)
	mapShadow = color.NRGBA{3, 45, 52, 97}
	keyHi     = rgb(0x3fd072)
	key       = rgb(0x18a84c)
	keyLo     = rgb(0x11903f)
	keyLip    = rgb(0x0b6a2e)
	keyShade  = color.NRGBA{6, 80, 32, 178}
	red       = rgb(0xe5323f)
)

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

// Shape is one part of a face's drawing: an SVG path as the server draws it (M/L/Z, Web
// Mercator, y down), lit when it is the place.
type Shape struct {
	D   string
	Lit bool
}

// Data is what a card says. Name is the challenger; Kind the map's small gold tag (its kind, or
// the place around it); Face its place inside its parent; Scores the rounds, one square each.
type Data struct {
	Name   string
	Kind   string
	Map    string
	Face   []Shape
	Total  int
	Scores []int
}

// Draw makes the card as a PNG.
func Draw(d Data) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	ground(img)

	const left = 64
	star(img, left, 38, 54)
	x := lettering(img, display, 46, left+68, 82, "Geo", white, 0, true)
	lettering(img, display, 46, x, 82, "Shafaq", goldText, 0, true)

	who := fitted(body, 40, Width-2*left, d.Name+" sizni chaqirdi")
	lettering(img, body, who.size, left, 158, who.text, white, 0, false)

	// the map: its face, and beside it the gold tag over its name
	const faceY, faceS = 190, 232
	face(img, d.Face, left, faceY, faceS)
	tx := left + faceS + 36
	mid := faceY + faceS/2
	if d.Kind != "" {
		tag(img, tx, mid-58, Width-left-tx, strings.ToUpper(d.Kind))
	}
	name := fitted(display, 68, Width-left-tx-12, d.Map)
	lettering(img, display, name.size, tx, mid+58, name.text, white, 0, true)

	// the score to beat, big and gold, and its squares
	x = lettering(img, display, 112, left, 540, Group(d.Total), goldText, 0, true)
	lettering(img, body, 40, x+20, 540, "ball", onGround3, 0, false)
	squares(img, left, 562, d.Scores)

	button(img, Width-left-340, 452, 340, 100)

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

// ground is the turquoise, deeper at the bottom, with a soft glow right of the middle (the page's
// body: a radial glow over a linear gradient).
func ground(img *image.RGBA) {
	cx, cy, rx, ry := 0.6*Width, 0.45*Height, 1.2*Width, 0.7*Height
	for y := range Height {
		t := float64(y) / (Height - 1)
		base := [3]float64{mix(groundTop.R, groundBot.R, t), mix(groundTop.G, groundBot.G, t), mix(groundTop.B, groundBot.B, t)}
		for x := range Width {
			c := base
			d := math.Hypot((float64(x)-cx)/rx, (float64(y)-cy)/ry)
			if d < 0.6 {
				a := float64(glow.A) / 255 * (1 - d/0.6)
				c[0] += (float64(glow.R) - c[0]) * a
				c[1] += (float64(glow.G) - c[1]) * a
				c[2] += (float64(glow.B) - c[2]) * a
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(c[0]+0.5), uint8(c[1]+0.5), uint8(c[2]+0.5), 255
		}
	}
}

func mix(a, b uint8, t float64) float64 { return float64(a) + (float64(b)-float64(a))*t }

// gradient is a vertical gradient between y0 and y1, for filling shapes.
type gradient struct {
	y0, y1 float64
	stops  []color.NRGBA // evenly spaced
}

func (g gradient) ColorModel() color.Model { return color.NRGBAModel }
func (g gradient) Bounds() image.Rectangle {
	return image.Rect(-1e6, -1e6, 1e6, 1e6)
}
func (g gradient) At(_, y int) color.Color {
	t := min(max((float64(y)-g.y0)/(g.y1-g.y0), 0), 1) * float64(len(g.stops)-1)
	i := min(int(t), len(g.stops)-2)
	a, b, f := g.stops[i], g.stops[i+1], t-float64(i)
	return color.NRGBA{uint8(mix(a.R, b.R, f) + 0.5), uint8(mix(a.G, b.G, f) + 0.5), uint8(mix(a.B, b.B, f) + 0.5), uint8(mix(a.A, b.A, f) + 0.5)}
}

// ---------- lettering ----------

type line struct {
	text string
	size float64
}

// fitted shrinks a line to fit, then cuts it: a long name or a long map stays on the card.
func fitted(f *opentype.Font, size float64, maxW int, s string) line {
	s = known(f, s)
	for sz := size; sz >= size*0.6; sz -= 2 {
		if width(f, sz, s) <= maxW {
			return line{s, sz}
		}
	}
	small := size * 0.6
	const dots = "..." // Onest has no "…", and M PLUS draws it centred, the Japanese way
	for width(f, small, s+dots) > maxW && utf8.RuneCountInString(s) > 1 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return line{strings.TrimRight(s, " ") + dots, small}
}

// known leaves out what the font can't draw (a chosen name may hold an emoji), and the space
// that stood beside it.
func known(f *opentype.Font, s string) string {
	face := newFace(f, 20)
	defer face.Close()
	out := strings.Map(func(r rune) rune {
		if _, ok := face.GlyphAdvance(r); ok {
			return r
		}
		return -1
	}, s)
	return strings.Join(strings.Fields(out), " ")
}

func newFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
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

// lean is the look's 8° forward lean of titles, numbers and keys.
var lean = math.Tan(8 * math.Pi / 180)

// lettering draws s with its baseline at y and says where it ended. spacing adds that share
// of the size between letters (a tag's wide capitals); leaning ones lean 8° forward, as the
// game's titles do, and float on its stacked shadow.
func lettering(img *image.RGBA, f *opentype.Font, size float64, x, y int, s string, c color.Color, spacing float64, leaning bool) int {
	m, asc, w := glyphs(f, size, s, spacing)
	if leaning {
		m = leaned(m, asc)
		mask(img, m, x, y-asc+2, stack)
		mask(img, m, x, y-asc+5, stack2)
	}
	mask(img, m, x, y-asc, c)
	return x + w
}

// glyphs draws s as a mask: its ascent (where the baseline lies) and its advance.
func glyphs(f *opentype.Font, size float64, s string, spacing float64) (*image.Alpha, int, int) {
	face := newFace(f, size)
	defer face.Close()
	met := face.Metrics()
	asc, desc := met.Ascent.Ceil(), met.Descent.Ceil()
	w := font.MeasureString(face, s).Ceil() + int(spacing*size*float64(utf8.RuneCountInString(s)))
	pad := int(size * 0.3) // the lean's room on the right
	m := image.NewAlpha(image.Rect(0, 0, w+pad, asc+desc))
	dr := font.Drawer{Dst: m, Src: image.Opaque, Face: face, Dot: fixed.P(0, asc)}
	if spacing == 0 {
		dr.DrawString(s)
	} else {
		for _, r := range s {
			dr.DrawString(string(r))
			dr.Dot.X += fixed.Int26_6(spacing * size * 64)
		}
	}
	return m, asc, dr.Dot.X.Ceil()
}

// leaned skews a mask forward round its baseline: the top goes right, as CSS skewX(-8deg) does.
func leaned(m *image.Alpha, asc int) *image.Alpha {
	b := m.Bounds()
	out := image.NewAlpha(b)
	// out(x, y) = m(x - lean·(asc - y), y): Transform maps the source into the destination
	aff := f64.Aff3{1, -lean, lean * float64(asc), 0, 1, 0}
	draw.BiLinear.Transform(out, aff, m, b, draw.Src, nil)
	return out
}

func mask(img *image.RGBA, m *image.Alpha, x, y int, c color.Color) {
	r := m.Bounds().Add(image.Pt(x, y))
	draw.DrawMask(img, r, &image.Uniform{c}, image.Point{}, m, image.Point{}, draw.Over)
}

// ---------- the parts ----------

// star is the game's mark: the eight-point star, gold, its heart the plaque's deep turquoise
// (GameBar.svelte's paths, on a 32-unit square).
func star(img *image.RGBA, x, y, size int) {
	k := float32(size) / 32
	ray := []float32{16, 1, 19.9, 6.6, 26.6, 5.4, 25.4, 12.1, 31, 16, 25.4, 19.9, 26.6, 26.6, 19.9, 25.4,
		16, 31, 12.1, 25.4, 5.4, 26.6, 6.6, 19.9, 1, 16, 6.6, 12.1, 5.4, 5.4, 12.1, 6.6}
	core := []float32{16, 8, 18.3, 11.3, 22.2, 10.6, 21.5, 14.5, 24.8, 16, 21.5, 18.3, 22.2, 22.2, 18.3, 21.5,
		16, 24.8, 13.7, 21.5, 9.8, 22.2, 10.5, 18.3, 7.2, 16, 10.5, 13.7, 9.8, 9.8, 13.7, 10.5}
	poly := func(pts []float32, dy int, c color.Color) {
		ras := vector.NewRasterizer(size, size+dy)
		for i := 0; i < len(pts); i += 2 {
			px, py := pts[i]*k, pts[i+1]*k+float32(dy)
			if i == 0 {
				ras.MoveTo(px, py)
			} else {
				ras.LineTo(px, py)
			}
		}
		ras.ClosePath()
		ras.Draw(img, image.Rect(x, y, x+size, y+size+dy), &image.Uniform{c}, image.Point{})
	}
	poly(ray, 3, stack) // it stands on the same stacked shadow as the name
	poly(ray, 0, gold)
	poly(core, 0, starIn)
	disc(img, float32(x)+16*k, float32(y)+16*k, 3.2*k, goldHi)
}

// tag is the small gold tag over a title that names a kind or a level, its capitals spaced.
func tag(img *image.RGBA, x, y, maxW int, s string) {
	const h, padX, spacing = 42, 16, 0.12
	l := fitted(display, 22, maxW-2*padX-int(22*spacing*float64(utf8.RuneCountInString(s))), s)
	m, asc, w := glyphs(display, l.size, l.text, spacing)
	m = leaned(m, asc)
	tw := w + 2*padX
	round(img, x, y+3, tw, h, 11, goldLo)
	roundSrc(img, x, y, tw, h, 11, gradient{float64(y), float64(y + h), []color.NRGBA{goldHi, gold}})
	base := y + h/2 + int(l.size*0.36)
	mask(img, m, x+padX, base-asc, kickInk)
}

// squares: one a round, as the game's share text has them (🟩🟨🟥), each with its lip.
func squares(img *image.RGBA, x, y int, scores []int) {
	const size, gap = 36, 10
	if len(scores) > 15 {
		scores = scores[:15]
	}
	for i, sc := range scores {
		c, lip := red, rgb(0xa8202b)
		switch {
		case sc >= 4000:
			c, lip = key, keyLip
		case sc >= 2500:
			c, lip = gold, goldLo
		}
		sx := x + i*(size+gap)
		round(img, sx, y+4, size, size, 9, lip)
		round(img, sx, y, size, size, 9, c)
	}
}

// button is the one green key, "O‘YNASH", raised on its darker lip, as in the game.
func button(img *image.RGBA, x, y, w, h int) {
	const r = 26
	round(img, x, y+16, w, h, r, color.NRGBA{2, 24, 10, 60}) // its glow on the ground
	round(img, x, y+9, w, h, r, keyLip)
	round(img, x, y, w, h, r, color.NRGBA{255, 255, 255, 140}) // the light along its top edge
	roundSrc(img, x, y+3, w, h-3, r, gradient{float64(y), float64(y + h), []color.NRGBA{keyHi, key, keyLo}})
	const label = "O‘YNASH"
	size := 42.0
	m, asc, tw := glyphs(display, size, label, 0.02)
	m = leaned(m, asc)
	sx := x + (w-tw)/2
	base := y + h/2 + int(size*0.36)
	mask(img, m, sx, base-asc+3, keyShade)
	mask(img, m, sx, base-asc, white)
}

// round fills a rounded rectangle, its edges smooth.
func round(img *image.RGBA, x, y, w, h, r int, c color.Color) {
	roundSrc(img, x, y, w, h, r, &image.Uniform{c})
}

func roundSrc(img *image.RGBA, x, y, w, h, r int, src image.Image) {
	ras := vector.NewRasterizer(w, h)
	rr := float32(r)
	const k = 0.5523 // a quarter circle as a curve
	c := rr * (1 - k)
	fw, fh := float32(w), float32(h)
	ras.MoveTo(rr, 0)
	ras.LineTo(fw-rr, 0)
	ras.CubeTo(fw-c, 0, fw, c, fw, rr)
	ras.LineTo(fw, fh-rr)
	ras.CubeTo(fw, fh-c, fw-c, fh, fw-rr, fh)
	ras.LineTo(rr, fh)
	ras.CubeTo(c, fh, 0, fh-c, 0, fh-rr)
	ras.LineTo(0, rr)
	ras.CubeTo(0, c, c, 0, rr, 0)
	ras.ClosePath()
	ras.Draw(img, image.Rect(x, y, x+w, y+h), src, image.Pt(x, y))
}

func disc(img *image.RGBA, cx, cy, r float32, c color.Color) {
	b := image.Rect(int(cx-r)-1, int(cy-r)-1, int(cx+r)+2, int(cy+r)+2)
	ras := vector.NewRasterizer(b.Dx(), b.Dy())
	circle(ras, cx-float32(b.Min.X), cy-float32(b.Min.Y), r)
	ras.Draw(img, b, &image.Uniform{c}, image.Point{})
}

func circle(ras *vector.Rasterizer, cx, cy, r float32) {
	const k = 0.5523
	ras.MoveTo(cx+r, cy)
	ras.CubeTo(cx+r, cy+k*r, cx+k*r, cy+r, cx, cy+r)
	ras.CubeTo(cx-k*r, cy+r, cx-r, cy+k*r, cx-r, cy)
	ras.CubeTo(cx-r, cy-k*r, cx-k*r, cy-r, cx, cy-r)
	ras.CubeTo(cx+k*r, cy-r, cx+r, cy-k*r, cx+r, cy)
	ras.ClosePath()
}
