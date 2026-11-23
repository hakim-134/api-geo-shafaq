package card

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/vector"
)

// The map's face, as the game draws it on the ground (PlaceInParent.svelte): its parent's parts
// as milk tiles with turquoise seams, the place gold, the whole standing on a soft shadow. Lines
// are thicker than on a page: a link preview is shown at a third of its size.
const (
	faceOut  = 3.0 // the parent's outline
	faceSeam = 2.0
	faceEdge = 3.0 // round the gold place
)

var faceLine = rgb(0x0a5864)

type pt struct{ x, y float64 }

type ring []pt

type part struct {
	rings []ring
	lit   bool
}

// face draws the parts in a size × size square at x, y.
func face(img *image.RGBA, shapes []Shape, x, y, size int) {
	parts := make([]part, 0, len(shapes))
	for _, s := range shapes {
		if rs := parse(s.D); len(rs) > 0 {
			parts = append(parts, part{rings: rs, lit: s.Lit})
		}
	}
	if len(parts) == 0 {
		return
	}
	parts = trimmed(parts)

	// fit the parts in the square, centred, a little room for the outline
	b := boxOf(allRings(parts))
	pad := faceOut + 2
	k := (float64(size) - 2*pad) / math.Max(b[2]-b[0], b[3]-b[1])
	ox := float64(x) + (float64(size)-(b[2]-b[0])*k)/2 - b[0]*k
	oy := float64(y) + (float64(size)-(b[3]-b[1])*k)/2 - b[1]*k
	for i := range parts {
		for j, r := range parts[i].rings {
			out := make(ring, len(r))
			for n, p := range r {
				out[n] = pt{ox + p.x*k, oy + p.y*k}
			}
			parts[i].rings[j] = out
		}
	}

	all := allRings(parts)
	area := image.Rect(x-8, y-8, x+size+8, y+size+12)
	fill(img, area, all, pt{0, 5}, &image.Uniform{mapShadow})
	fill(img, area, all, pt{}, &image.Uniform{faceLine})
	stroke(img, area, all, 2*faceOut, &image.Uniform{faceLine})
	milk := gradient{float64(y), float64(y + size), []color.NRGBA{tileA, tileB}}
	golden := gradient{float64(y), float64(y + size), []color.NRGBA{goldHi, gold}}
	var lit []ring
	for _, p := range parts {
		if p.lit {
			fill(img, area, p.rings, pt{}, golden)
			lit = append(lit, p.rings...)
		} else {
			fill(img, area, p.rings, pt{}, milk)
		}
	}
	stroke(img, area, all, faceSeam, &image.Uniform{seam})
	if len(lit) > 0 {
		stroke(img, area, lit, faceEdge, &image.Uniform{goldLo})
	}

	// a gold place too small to see gets a ring round it, as in the game
	for _, p := range parts {
		if !p.lit || len(lit) == len(all) {
			continue
		}
		main := mainBox(p.rings)
		big := math.Max(main[2]-main[0], main[3]-main[1])
		if big >= 14 && big/float64(size) >= 0.06 {
			continue
		}
		cx, cy := (main[0]+main[2])/2, (main[1]+main[3])/2
		r := math.Max(math.Max(float64(size)*0.075, big*0.9), 14)
		ringAt(img, area, cx, cy, r, 4, goldLo)
		if big < 6 {
			disc(img, float32(cx), float32(cy), float32(r*0.42), gold)
		}
	}
}

// parse reads an SVG path of M, L and Z with plain numbers, as ST_AsSVG writes it.
func parse(d string) []ring {
	var out []ring
	var cur ring
	var num []float64
	end := func() {
		if len(cur) >= 3 {
			out = append(out, cur)
		}
		cur, num = nil, nil
	}
	for _, t := range strings.Fields(d) {
		switch t {
		case "M":
			end()
		case "L":
		case "Z", "z":
			end()
		default:
			v, err := strconv.ParseFloat(t, 64)
			if err != nil {
				continue
			}
			if num = append(num, v); len(num) == 2 {
				cur = append(cur, pt{num[0], num[1]})
				num = num[:0]
			}
		}
	}
	end()
	return out
}

type box [4]float64

func boxOf(rs []ring) box {
	b := box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, r := range rs {
		for _, p := range r {
			b[0], b[1] = math.Min(b[0], p.x), math.Min(b[1], p.y)
			b[2], b[3] = math.Max(b[2], p.x), math.Max(b[3], p.y)
		}
	}
	return b
}

func (b box) area() float64 { return (b[2] - b[0]) * (b[3] - b[1]) }

func (b box) meets(o box) bool { return b[0] < o[2] && b[2] > o[0] && b[1] < o[3] && b[3] > o[1] }

// mainBox is the box of a part's largest piece.
func mainBox(rs []ring) box {
	var best box
	for i, r := range rs {
		if b := boxOf([]ring{r}); i == 0 || b.area() > best.area() {
			best = b
		}
	}
	return best
}

func allRings(parts []part) []ring {
	var out []ring
	for _, p := range parts {
		out = append(out, p.rings...)
	}
	return out
}

// trimmed leaves out the far crumbs (an enclave's dot away from the rest), as the game does, so
// the picture is as big as it can be.
func trimmed(parts []part) []part {
	all := box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range parts {
		m := mainBox(p.rings)
		all = box{math.Min(all[0], m[0]), math.Min(all[1], m[1]), math.Max(all[2], m[2]), math.Max(all[3], m[3])}
	}
	out := make([]part, 0, len(parts))
	for _, p := range parts {
		var keep []ring
		for _, r := range p.rings {
			if boxOf([]ring{r}).meets(all) {
				keep = append(keep, r)
			}
		}
		if len(keep) > 0 {
			out = append(out, part{rings: keep, lit: p.lit})
		}
	}
	return out
}

// fill paints the rings (holes wound the other way stay holes), moved by off.
func fill(img *image.RGBA, area image.Rectangle, rs []ring, off pt, src image.Image) {
	area = area.Intersect(img.Bounds()) // the rasterizer doesn't clip
	ras := vector.NewRasterizer(area.Dx(), area.Dy())
	for _, r := range rs {
		for i, p := range r {
			x, y := float32(p.x+off.x-float64(area.Min.X)), float32(p.y+off.y-float64(area.Min.Y))
			if i == 0 {
				ras.MoveTo(x, y)
			} else {
				ras.LineTo(x, y)
			}
		}
		ras.ClosePath()
	}
	ras.Draw(img, area, src, area.Min)
}

// stroke draws the rings' lines w wide: a quad along each segment and a small octagon at each
// corner, all wound the same way so that overlaps add up instead of cancelling.
func stroke(img *image.RGBA, area image.Rectangle, rs []ring, w float64, src image.Image) {
	area = area.Intersect(img.Bounds())
	ras := vector.NewRasterizer(area.Dx(), area.Dy())
	h := w / 2
	at := pt{float64(area.Min.X), float64(area.Min.Y)}
	for _, r := range rs {
		for i, a := range r {
			b := r[(i+1)%len(r)]
			dx, dy := b.x-a.x, b.y-a.y
			l := math.Hypot(dx, dy)
			if l > 0 {
				nx, ny := -dy/l*h, dx/l*h
				poly(ras, at, []pt{{a.x + nx, a.y + ny}, {b.x + nx, b.y + ny}, {b.x - nx, b.y - ny}, {a.x - nx, a.y - ny}})
			}
			oct := make([]pt, 8)
			for j := range oct {
				t := float64(j) * math.Pi / 4
				oct[j] = pt{a.x + h*math.Cos(t), a.y + h*math.Sin(t)}
			}
			poly(ras, at, oct)
		}
	}
	ras.Draw(img, area, src, area.Min)
}

// poly adds a closed polygon, wound one way whatever its order.
func poly(ras *vector.Rasterizer, at pt, p []pt) {
	s := 0.0
	for i, a := range p {
		b := p[(i+1)%len(p)]
		s += a.x*b.y - b.x*a.y
	}
	for n := range p {
		i := n
		if s < 0 {
			i = len(p) - 1 - n
		}
		x, y := float32(p[i].x-at.x), float32(p[i].y-at.y)
		if n == 0 {
			ras.MoveTo(x, y)
		} else {
			ras.LineTo(x, y)
		}
	}
	ras.ClosePath()
}

// ringAt is a circle's line, w wide.
func ringAt(img *image.RGBA, area image.Rectangle, cx, cy, r, w float64, c color.Color) {
	const n = 64
	circ := make(ring, n)
	for i := range circ {
		t := float64(i) * 2 * math.Pi / n
		circ[i] = pt{cx + r*math.Cos(t), cy + r*math.Sin(t)}
	}
	stroke(img, area.Union(image.Rect(int(cx-r-w), int(cy-r-w), int(cx+r+w)+1, int(cy+r+w)+1)), []ring{circ}, w, &image.Uniform{c})
}
