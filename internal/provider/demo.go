package provider

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
)

// Demo is a drawn test panorama: sky, ground and the four directions. It costs nothing, so
// development and tests never spend Yandex views. Its reference is opaque and signed, like
// our own imagery will be: the browser cannot tell where it is.
type Demo struct {
	Secret  []byte
	BaseURL string // the API's public address, for the image URL
}

func (Demo) Name() string { return "demo" }

func (Demo) Capabilities() Capabilities {
	return Capabilities{Move: false, HidesCoords: true, Credit: "Shafaq Geo demo"}
}

func (d Demo) View(p Pano, _ geo.Point, _ float64, heading float64) View {
	ref := d.sign(p.ID)
	return View{Provider: "demo", Ref: ref, ImageURL: d.BaseURL + "/v1/pano/demo/" + ref, Heading: heading}
}

var ErrBadRef = errors.New("bad panorama reference")

func (d Demo) sign(id int64) string {
	b := make([]byte, 8, 8+12)
	binary.BigEndian.PutUint64(b, uint64(id))
	mac := hmac.New(sha256.New, d.Secret)
	mac.Write(b)
	b = mac.Sum(b)[:8+12]
	return base64.RawURLEncoding.EncodeToString(b)
}

// Open checks a reference and returns the panorama id in it.
func (d Demo) Open(ref string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(ref)
	if err != nil || len(b) != 20 {
		return 0, ErrBadRef
	}
	id := int64(binary.BigEndian.Uint64(b[:8]))
	if !hmac.Equal([]byte(d.sign(id)), []byte(ref)) {
		return 0, ErrBadRef
	}
	return id, nil
}

// DemoImage draws the equirectangular test panorama for a library panorama as a JPEG: a
// sky and ground, a street of buildings, a horizon grid and N/E/S/W where they really are,
// so the compass can be checked by eye. The number tells rounds apart. A real image (not
// SVG) because panorama viewers decode it as a bitmap, as they will our own photos.
func DemoImage(id int64) ([]byte, error) {
	const w, h = 2048, 1024
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	hue := float64(id * 47 % 360)
	for y := 0; y < h; y++ {
		var c color.RGBA
		if y < h/2 {
			c = hsl(200, 0.65, 0.32+0.45*float64(y)/float64(h/2))
		} else {
			c = hsl(hue, 0.3, 0.42-0.25*float64(y-h/2)/float64(h/2))
		}
		draw.Draw(img, image.Rect(0, y, w, y+1), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	// buildings on the horizon, from the id, so rounds differ
	for x, i := 0, 0; x < w; i++ {
		bw := 30 + int((id*31+int64(i)*17)%70)
		bh := 40 + int((id*13+int64(i)*29)%160)
		c := hsl(math.Mod(hue+float64(i*11), 360), 0.2, 0.55+float64(i*7%25)/100)
		draw.Draw(img, image.Rect(x, h/2-bh, x+bw-3, h/2), &image.Uniform{c}, image.Point{}, draw.Src)
		x += bw
	}
	grid := color.NRGBA{255, 255, 255, 60}
	for lon := 0; lon < 360; lon += 15 {
		px := lon * w / 360
		draw.Draw(img, image.Rect(px, h/2, px+2, h), &image.Uniform{grid}, image.Point{}, draw.Over)
	}
	// The viewer puts the image's centre at heading 0 (north): north sits at x = w/2.
	big, err := face(110)
	if err != nil {
		return nil, err
	}
	for i, label := range []string{"N", "E", "S", "W"} {
		c := color.RGBA{255, 255, 255, 255}
		if label == "N" {
			c = color.RGBA{206, 17, 38, 255}
		}
		text(img, big, label, (w/2+i*w/4)%w, h/2-260, c)
	}
	small, err := face(48)
	if err != nil {
		return nil, err
	}
	text(img, small, fmt.Sprintf("DEMO #%d", id), w/2, h/2+170, color.NRGBA{255, 255, 255, 220})

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func face(size float64) (font.Face, error) {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// text draws s centred on x, with its baseline at y.
func text(img *image.RGBA, f font.Face, s string, x, y int, c color.Color) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f}
	width := d.MeasureString(s).Round()
	d.Dot = fixed.P(x-width/2, y)
	d.DrawString(s)
}

func hsl(hDeg, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(hDeg, 360) / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g = c, x
	case hp < 2:
		r, g = x, c
	case hp < 3:
		g, b = c, x
	case hp < 4:
		g, b = x, c
	case hp < 5:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := l - c/2
	to := func(v float64) uint8 { return uint8(math.Round(math.Min(math.Max(v+m, 0), 1) * 255)) }
	return color.RGBA{to(r), to(g), to(b), 255}
}
