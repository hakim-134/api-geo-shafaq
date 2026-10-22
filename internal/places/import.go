package places

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// The admin levels the game uses, and how much each is simplified for its outline (degrees).
var levels = map[string]int{"2": 2, "3": 3, "4": 4, "6": 6}

func simplifyFor(kind string) float64 {
	switch kind {
	case "country":
		return 0.01
	case "region":
		return 0.004
	default:
		return 0.0007
	}
}

// Import reads the boundaries from an .osm.pbf extract into the places table, links each
// place to its parent and draws the outlines. Running it again updates the same rows.
func Import(ctx context.Context, pool *pgxpool.Pool, pbf string) error {
	bounds, err := ReadBoundaries(ctx, pbf, levels)
	if err != nil {
		return fmt.Errorf("read %s: %w", pbf, err)
	}
	Log(bounds)

	q := store.New(pool)
	slugs := map[string]bool{}
	saved := 0
	for _, b := range bounds {
		if b.Level == 2 && b.Tags.Find("ISO3166-1") != "UZ" {
			continue
		}
		if iso := b.Tags.Find("ISO3166-2"); iso != "" && !strings.HasPrefix(iso, "UZ-") {
			continue // a neighbour's region that came in with the shared border
		}
		uz, ru, en := b.Names()
		if uz == "" || len(b.Lines) == 0 {
			continue
		}
		kind := b.Kind()
		slug := Slug(uz)
		if kind == "country" {
			slug = "uzbekistan"
		}
		if slugs[slug] {
			slug += "-" + strconv.FormatInt(b.OSMID, 10)
		}
		if en == "" {
			en = uz
		}
		if ru == "" {
			ru = uz
		}
		_, err := q.UpsertPlace(ctx, store.UpsertPlaceParams{
			Kind: kind, Slug: slug, NameUz: uz, NameRu: ru, NameEn: en,
			OsmID: &b.OSMID, Geojson: b.GeoJSON(), Simplify: simplifyFor(kind),
		})
		if err != nil {
			// a boundary cut by the extract's edge has no closed ring; it is a neighbour's
			slog.Debug("place skipped", "osm", b.OSMID, "name", uz, "err", err)
			continue
		}
		slugs[slug] = true
		saved++
	}
	slog.Info("places saved", "n", saved)

	dropped, err := q.DeletePlacesOutside(ctx)
	if err != nil {
		return err
	}
	slog.Info("places outside Uzbekistan dropped", "n", dropped)
	if err := q.LinkPlaceParents(ctx); err != nil {
		return err
	}
	if err := drawOutlines(ctx, q); err != nil {
		return err
	}
	counts, err := q.CountPlaces(ctx)
	if err != nil {
		return err
	}
	for _, c := range counts {
		slog.Info("places", "kind", c.Kind, "n", c.N)
	}
	return nil
}

func drawOutlines(ctx context.Context, q *store.Queries) error {
	rows, err := q.ListPlacesForOutline(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		svg, err := OutlineSVG(r.Geojson)
		if err != nil {
			return fmt.Errorf("outline of place %d: %w", r.ID, err)
		}
		if err := q.SetPlaceOutline(ctx, store.SetPlaceOutlineParams{ID: r.ID, OutlineSvg: svg}); err != nil {
			return err
		}
	}
	return nil
}

// OutlineSVG draws a (Multi)Polygon as a small SVG path in a 100×100 box, north up, with the
// longitude squeezed so the shape looks as on a map. The game colours it with CSS.
func OutlineSVG(geojson string) (string, error) {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(geojson), &g); err != nil {
		return "", err
	}
	var polys [][][][2]float64
	switch g.Type {
	case "MultiPolygon":
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return "", err
		}
	case "Polygon":
		var p [][][2]float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil {
			return "", err
		}
		polys = [][][][2]float64{p}
	default:
		return "", fmt.Errorf("unexpected geometry %s", g.Type)
	}

	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range polys {
		for _, ring := range p {
			for _, c := range ring {
				minX, maxX = math.Min(minX, c[0]), math.Max(maxX, c[0])
				minY, maxY = math.Min(minY, c[1]), math.Max(maxY, c[1])
			}
		}
	}
	if math.IsInf(minX, 0) {
		return "", nil
	}
	k := math.Cos((minY + maxY) / 2 * math.Pi / 180)
	w, h := (maxX-minX)*k, maxY-minY
	scale := 96 / math.Max(w, h)
	offX, offY := (100-w*scale)/2, (100-h*scale)/2

	var d strings.Builder
	for _, p := range polys {
		for _, ring := range p {
			var lastX, lastY int
			for i, c := range ring {
				x := int(math.Round((offX + (c[0]-minX)*k*scale) * 10))
				y := int(math.Round((offY + (maxY-c[1])*scale) * 10))
				if i > 0 && x == lastX && y == lastY {
					continue
				}
				if i == 0 {
					d.WriteString("M")
				} else {
					d.WriteString("L")
				}
				fmt.Fprintf(&d, "%s %s", tenth(x), tenth(y))
				lastX, lastY = x, y
			}
			d.WriteString("Z")
		}
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path fill-rule="evenodd" d="` + d.String() + `"/></svg>`, nil
}

func tenth(v int) string {
	if v%10 == 0 {
		return strconv.Itoa(v / 10)
	}
	return strconv.FormatFloat(float64(v)/10, 'f', 1, 64)
}
