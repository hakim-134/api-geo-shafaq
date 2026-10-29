// Package places builds the place tree (country → region → district or city) from an
// OpenStreetMap extract, and answers "where is this point" after a round.
package places

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
)

// Boundary is one administrative relation with the linework of its member ways.
type Boundary struct {
	OSMID  int64
	Level  int
	Tags   osm.Tags
	ways   []osm.WayID
	Lines  [][][2]float64 // [lon, lat] per way
	Broken bool           // some member ways or nodes are missing from the extract
}

// ReadBoundaries reads the administrative boundaries of the given admin levels from an
// .osm.pbf file. It reads the file three times (relations, ways, nodes), keeping only what
// the boundaries need, so a country extract fits in little memory.
func ReadBoundaries(ctx context.Context, path string, levels map[string]int) ([]*Boundary, error) {
	var bounds []*Boundary
	wantWays := map[osm.WayID][]osm.NodeID{}

	err := scan(ctx, path, func(o osm.Object) {
		r, ok := o.(*osm.Relation)
		if !ok || r.Tags.Find("boundary") != "administrative" {
			return
		}
		level, ok := levels[r.Tags.Find("admin_level")]
		if !ok {
			return
		}
		b := &Boundary{OSMID: int64(r.ID), Level: level, Tags: r.Tags}
		for _, m := range r.Members {
			if m.Type == osm.TypeWay && (m.Role == "outer" || m.Role == "inner" || m.Role == "") {
				b.ways = append(b.ways, osm.WayID(m.Ref))
				wantWays[osm.WayID(m.Ref)] = nil
			}
		}
		bounds = append(bounds, b)
	}, false, false, true)
	if err != nil {
		return nil, err
	}

	wantNodes := map[osm.NodeID][2]float64{}
	err = scan(ctx, path, func(o osm.Object) {
		w, ok := o.(*osm.Way)
		if !ok {
			return
		}
		if _, want := wantWays[w.ID]; !want {
			return
		}
		ids := make([]osm.NodeID, len(w.Nodes))
		for i, n := range w.Nodes {
			ids[i] = n.ID
			wantNodes[n.ID] = [2]float64{}
		}
		wantWays[w.ID] = ids
	}, false, true, false)
	if err != nil {
		return nil, err
	}

	found := map[osm.NodeID]bool{}
	err = scan(ctx, path, func(o osm.Object) {
		n, ok := o.(*osm.Node)
		if !ok {
			return
		}
		if _, want := wantNodes[n.ID]; want {
			wantNodes[n.ID] = [2]float64{n.Lon, n.Lat}
			found[n.ID] = true
		}
	}, true, false, false)
	if err != nil {
		return nil, err
	}

	for _, b := range bounds {
		for _, wid := range b.ways {
			ids := wantWays[wid]
			if len(ids) < 2 {
				b.Broken = true
				continue
			}
			line := make([][2]float64, 0, len(ids))
			for _, id := range ids {
				if !found[id] {
					b.Broken = true
					continue
				}
				line = append(line, wantNodes[id])
			}
			if len(line) >= 2 {
				b.Lines = append(b.Lines, line)
			}
		}
	}
	return bounds, nil
}

func scan(ctx context.Context, path string, fn func(osm.Object), nodes, ways, relations bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s := osmpbf.New(ctx, f, runtime.GOMAXPROCS(-1))
	defer s.Close()
	s.SkipNodes = !nodes
	s.SkipWays = !ways
	s.SkipRelations = !relations
	for s.Scan() {
		fn(s.Object())
	}
	if err := s.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// GeoJSON is the boundary's linework as a GeoJSON MultiLineString; PostGIS's ST_BuildArea
// turns it into the polygon with its holes.
func (b *Boundary) GeoJSON() string {
	g := struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	}{"MultiLineString", b.Lines}
	out, _ := json.Marshal(g)
	return string(out)
}

// Names returns the Uzbek (Latin), Russian and English names, cleaned up: the Uzbek
// letters o‘ and g‘ with the proper mark, and "tumani", "viloyati", "shahri" in lower case.
func (b *Boundary) Names() (uz, ru, en string) {
	uz = firstOf(b.Tags, "name:uz", "name:uz-Latn", "name")
	ru = firstOf(b.Tags, "name:ru")
	en = firstOf(b.Tags, "name:en")
	return CleanUz(uz), ru, en
}

// uzFixes are Uzbek names OpenStreetMap has wrong, by slug (the slug stays, so old games and
// maps keep their place): Karakalpak or Russian spellings, a missing o‘, a straight
// apostrophe for ʼ, towns without "shahri". Read through by hand on 2026-10-29 (P1028 4.2's
// second review); migration 00008 put them in the database.
var uzFixes = map[string]string{
	"angren":            "Angren shahri",
	"bekobod":           "Bekobod shahri",
	"ohangaron":         "Ohangaron shahri",
	"nurafshon-toytepa": "Nurafshon shahri",
	"bozataw-rayoni":    "Bo‘zatov tumani",
	"ellikqala-tumani":  "Ellikqalʼa tumani",
	"tuproqqala-tumani": "Tuproqqalʼa tumani",
	"kegeili-tumani":    "Kegeyli tumani",
	"moynoq-tumani":     "Mo‘ynoq tumani",
	"qoshkopir-tumani":  "Qo‘shko‘pir tumani",
	"ugrut-tumani":      "Urgut tumani",
	"showot-tumani":     "Shovot tumani",
	"qonlikol-tumani":   "Qanliko‘l tumani",
}

// FixUz is the place's Uzbek name, corrected where OpenStreetMap's is wrong.
func FixUz(slug, uz string) string {
	if fixed, ok := uzFixes[slug]; ok {
		return fixed
	}
	return uz
}

func firstOf(tags osm.Tags, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(tags.Find(k)); v != "" {
			return v
		}
	}
	return ""
}

var uzMarks = strings.NewReplacer(
	"o'", "o‘", "O'", "O‘", "g'", "g‘", "G'", "G‘",
	"oʻ", "o‘", "Oʻ", "O‘", "gʻ", "g‘", "Gʻ", "G‘",
	"o`", "o‘", "O`", "O‘", "g`", "g‘", "G`", "G‘",
	"o’", "o‘", "O’", "O‘", "g’", "g‘", "G’", "G‘",
	"ʼ", "’", "'", "’",
)

var uzWords = strings.NewReplacer(
	" Tumani", " tumani", " Viloyati", " viloyati", " Shahri", " shahri",
	" shahri hokimiyati", " shahri",
)

// CleanUz fixes the apostrophes and capitals OSM names come with.
func CleanUz(s string) string {
	return uzWords.Replace(uzMarks.Replace(strings.TrimSpace(s)))
}

// enSuffix is how OSM's English names end: "District", "district", "Distric", "tumani",
// "City", "shahri", or nothing.
var enSuffix = regexp.MustCompile(`(?i)\s+(district|distric|tumani|city|shahri)$`)

// CleanEn gives English names one form, as the result card and hints show them: a district
// (its Uzbek name ends in "tumani") is "Yunusabad District", a city is just "Samarkand".
func CleanEn(kind, uz, en string) string {
	en = strings.TrimSpace(enSuffix.ReplaceAllString(strings.TrimSpace(en), ""))
	if kind == "district" && strings.HasSuffix(uz, " tumani") {
		en += " District"
	}
	return en
}

// Kind decides what a boundary is in the game's terms.
func (b *Boundary) Kind() string {
	uz, _, en := b.Names()
	low := strings.ToLower(uz + " " + en)
	switch {
	case b.Level <= 2:
		return "country"
	case b.Tags.Find("ISO3166-2") == "UZ-TK":
		return "city" // Tashkent: a city at region level
	case b.Level <= 4:
		return "region"
	case strings.Contains(low, "tumani") || strings.Contains(low, "district"):
		return "district"
	case strings.Contains(low, " shahri") || strings.Contains(low, " city") ||
		b.Tags.Find("place") == "city" || b.Tags.Find("place") == "town":
		return "city"
	default:
		return "district"
	}
}

// Slug is a URL-safe ASCII name from the Uzbek one: "yashnobod-tumani", "samarqand-viloyati".
func Slug(name string) string {
	name = strings.ToLower(name)
	var sb strings.Builder
	dash := false
	for _, r := range name {
		var out string
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			out = string(r)
		case r == '‘' || r == '’' || r == '\'' || r == 'ʻ' || r == '`':
			continue
		default:
			if t, ok := translit[r]; ok {
				out = t
			}
		}
		if out == "" {
			if !dash && sb.Len() > 0 {
				sb.WriteByte('-')
				dash = true
			}
			continue
		}
		sb.WriteString(out)
		dash = false
	}
	return strings.Trim(sb.String(), "-")
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "x", 'ц': "ts", 'ч': "ch", 'ш': "sh",
	'щ': "sh", 'ы': "i", 'э': "e", 'ю': "yu", 'я': "ya", 'ў': "o", 'қ': "q", 'ғ': "g", 'ҳ': "h",
	'á': "a", 'ó': "o", 'ú': "u", 'ı': "i", 'ń': "n", 'ğ': "g", 'ş': "sh", 'ç': "ch",
}

// Log summarises what was read.
func Log(bounds []*Boundary) {
	byLevel := map[int]int{}
	broken := 0
	for _, b := range bounds {
		byLevel[b.Level]++
		if b.Broken {
			broken++
		}
	}
	slog.Info("osm boundaries read", "by_level", fmt.Sprint(byLevel), "incomplete", broken)
}
