package maps

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// A mix is several maps played as one (P1111 1.3): Yunusobod, Buxoro viloyati and Samarqand
// viloyati in one game. Each round is one part's, the parts in turn, and is picked and scored
// as on that part's own map, so a small part is neither drowned by a big one's panoramas nor
// scored by the size of everything mixed.

// MixPrefix starts the slug of a mix: "mix-" and a hash of its parts.
const MixPrefix = "mix-"

// MaxParts is how many maps one mix may be asked for.
const MaxParts = 12

// ErrBadMix: the parts can't be mixed (none, too many, a mix or a player's own map among them).
var ErrBadMix = errors.New("bad mix")

// MixSlug is the slug of the mix of these parts: the same parts in any order, the same map.
func MixSlug(parts []string) string {
	sorted := slices.Sorted(slices.Values(parts))
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return MixPrefix + hex.EncodeToString(sum[:])[:12]
}

// Mix is the map of these parts together, made the first time they are asked for together.
// Parts asked twice count once, a part inside another is left out (Yunusobod with Toshkent:
// Toshkent), and one part left is that part's own map, not a mix.
func (c *Catalog) Mix(ctx context.Context, slugs []string) (*Map, error) {
	if len(slugs) == 0 || len(slugs) > MaxParts {
		return nil, ErrBadMix
	}
	var parts []*Map
	for _, slug := range slugs {
		if slug == "" {
			return nil, ErrBadMix
		}
		if slices.ContainsFunc(parts, func(p *Map) bool { return p.Slug == slug }) {
			continue
		}
		p, err := c.get(ctx, slug, true)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	tree, err := c.tree.Get(ctx)
	if err != nil {
		return nil, err
	}
	parts = outer(parts, tree)
	if len(parts) == 1 {
		return parts[0], nil
	}

	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = p.Slug
	}
	slug := MixSlug(names)
	m, err := c.Get(ctx, slug)
	if !errors.Is(err, ErrNotFound) {
		return m, err
	}
	var name [3][]string // uz, ru, en
	var placeIDs []int64
	for _, p := range parts {
		name[0], name[1], name[2] = append(name[0], p.Name.Uz), append(name[1], p.Name.Ru), append(name[2], p.Name.En)
		for _, id := range p.Places {
			if !slices.Contains(placeIDs, id) {
				placeIDs = append(placeIDs, id)
			}
		}
	}
	rulesJSON, _ := json.Marshal(rules.Default)
	pickJSON, _ := json.Marshal(struct {
		Parts []string `json:"parts"`
	}{Parts: names})
	err = pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		id, err := q.InsertMixMap(ctx, store.InsertMixMapParams{
			Slug: slug, NameUz: strings.Join(name[0], ", "), NameRu: strings.Join(name[1], ", "), NameEn: strings.Join(name[2], ", "),
			Rules: rulesJSON, Pick: pickJSON,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // made meanwhile, by another request for the same parts
		}
		if err != nil {
			return err
		}
		return q.AddMapPlaceIDs(ctx, store.AddMapPlaceIDsParams{MapID: id, PlaceIds: placeIDs})
	})
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, slug)
}

func (m *Map) mixPartError() error {
	if m.Private {
		return ErrNotFound
	}
	if m.Mix() || m.Owner != nil {
		return ErrBadMix
	}
	return nil
}

// outer leaves out a single-place part inside another's place. The country also covers
// maps of several places, like historic.
func outer(parts []*Map, t *places.Tree) []*Map {
	var out []*Map
	for _, p := range parts {
		if !slices.ContainsFunc(parts, func(q *Map) bool { return containsPart(t, q, p) }) {
			out = append(out, p)
		}
	}
	return out
}

func containsPart(t *places.Tree, outer, inner *Map) bool {
	if outer == inner || len(outer.Places) != 1 || len(inner.Places) == 0 {
		return false
	}
	place := t.ByID(outer.Places[0])
	if place == nil || (len(inner.Places) != 1 && place.Slug != "uzbekistan") {
		return false
	}
	for _, id := range inner.Places {
		p := t.ByID(id)
		for p != nil && p != place {
			p = p.Parent
		}
		if p == nil || (id == place.ID && outer.Slug != "uzbekistan") {
			return false
		}
	}
	return true
}

// loadMix makes a mix of its parts' own maps: all their panoramas (each knows its part), all
// their places, and a box round all of them, where the guess map opens.
func (c *Catalog) loadMix(ctx context.Context, m *Map) error {
	tree, err := c.tree.Get(ctx)
	if err != nil {
		return err
	}
	for _, slug := range m.Pick.Parts {
		p, err := c.get(ctx, slug, true)
		if err != nil {
			return fmt.Errorf("mix %s, part %s: %w", m.Slug, slug, err)
		}
		m.addPart(p)
	}
	size := func(id int64) float64 {
		if p := tree.ByID(id); p != nil {
			return p.SizeKm
		}
		return 0
	}
	slices.SortStableFunc(m.Places, func(a, b int64) int { return cmp.Compare(size(b), size(a)) })
	if p := tree.Around(m.Places); p != nil {
		m.Face = p.Slug
	}
	return nil
}

// addPart puts a part into a mix: its panoramas (each knows its part; one two parts share
// comes once, in the first), its box and its places.
func (m *Map) addPart(p *Map) {
	m.parts = append(m.parts, p)
	for _, cand := range p.Cands {
		if !m.ids[cand.ID] {
			cand.From = p
			m.add(cand)
		}
	}
	if p.BBox != (geo.BBox{}) {
		m.BBox = m.BBox.Extend(geo.Point{Lat: p.BBox[1], Lon: p.BBox[0]}).Extend(geo.Point{Lat: p.BBox[3], Lon: p.BBox[2]})
	}
	m.SizeKm = m.BBox.Diagonal()
	for _, id := range p.Places {
		if !slices.Contains(m.Places, id) {
			m.Places = append(m.Places, id)
		}
	}
}

// pickPart is a mix's round: the part with the fewest rounds in this game so far (ties in an
// order the seed shuffles, so a seed always gives the same game), picked as its own map picks.
// A part with nothing left gives way to the next.
func (m *Map) pickPart(rng *rand.Rand, allowed map[string]bool, used Used, d rules.Difficulty) (Candidate, bool) {
	turns := make([]int, len(m.parts))
	for i, p := range m.parts {
		if used.Rounds == nil {
			for id, seen := range used.IDs {
				if seen && p.ids[id] {
					turns[i]++
				}
			}
			continue
		}
		for _, id := range used.Rounds {
			if p.ids[id] {
				turns[i]++
			}
		}
	}
	order := rng.Perm(len(m.parts))
	slices.SortStableFunc(order, func(a, b int) int { return turns[a] - turns[b] })

	// no street twice, also when two parts share it (a long street across two districts)
	streets := map[int64]bool{}
	for s := range used.Streets {
		streets[s] = true
	}
	// shown means a round played it: a panorama that failed to open (in IDs only, so it isn't
	// picked again) was never seen, and its street is still fair for the replacement
	shown := used.IDs
	if used.Rounds != nil {
		shown = make(map[int64]bool, len(used.Rounds))
		for _, id := range used.Rounds {
			shown[id] = true
		}
	}
	for _, c := range m.Cands {
		if c.StreetID != 0 && shown[c.ID] {
			streets[c.StreetID] = true
		}
	}
	used.Streets = streets

	for _, i := range order {
		if c, ok := m.parts[i].PickAt(rng, allowed, used, d); ok {
			c.From = m.parts[i]
			return c, true
		}
	}
	return Candidate{}, false
}
