package maps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

var ErrNotFound = errors.New("map not found")

// Map is a playable map with its panoramas, ready for picking rounds.
type Map struct {
	ID       int64
	Slug     string
	Name     places.Text
	Desc     places.Text
	Official bool
	Rules    rules.Rules
	Pick     Pick
	SizeKm   float64  // D for scoring
	BBox     geo.BBox // where the guess map opens
	Cands    []Candidate
	groups   map[int64][]int // group key → indices into Cands
	Streets  int             // named streets, on a street map
	Outline  string          // the place's outline SVG, for maps of one place
}

// Candidate is one panorama a round can show.
type Candidate struct {
	provider.Pano
	StreetID int64
	Group    int64
}

// Providers lists the providers with panoramas in this map.
func (m *Map) Providers() []string {
	seen := []string{}
	for _, c := range m.Cands {
		if !slices.Contains(seen, c.Provider) {
			seen = append(seen, c.Provider)
		}
	}
	return seen
}

// Count is how many panoramas the given providers have here; nil = all.
func (m *Map) Count(allowed map[string]bool) int {
	if allowed == nil {
		return len(m.Cands)
	}
	n := 0
	for _, c := range m.Cands {
		if allowed[c.Provider] {
			n++
		}
	}
	return n
}

// Groups is how many groups (streets, districts, regions) the map has.
func (m *Map) Groups() int { return len(m.groups) }

// Catalog serves maps from memory, reloading each a few minutes after it was read, so the
// round picker never queries the whole library.
type Catalog struct {
	q   *store.Queries
	reg *provider.Registry
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	m      *Map
	loaded time.Time
}

func NewCatalog(q *store.Queries, reg *provider.Registry) *Catalog {
	return &Catalog{q: q, reg: reg, ttl: 5 * time.Minute, entries: map[string]*entry{}}
}

// Official lists the menu's maps.
func (c *Catalog) Official(ctx context.Context) ([]*Map, error) {
	rows, err := c.q.ListOfficialMaps(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Map, 0, len(rows))
	for _, r := range rows {
		m, err := c.Get(ctx, r.Slug)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Get returns a map by slug.
func (c *Catalog) Get(ctx context.Context, slug string) (*Map, error) {
	c.mu.Lock()
	e := c.entries[slug]
	c.mu.Unlock()
	if e != nil && time.Since(e.loaded) < c.ttl {
		return e.m, nil
	}
	m, err := c.load(ctx, slug)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[slug] = &entry{m: m, loaded: time.Now()}
	c.mu.Unlock()
	return m, nil
}

// ByID finds a loaded map by id, loading it if needed.
func (c *Catalog) ByID(ctx context.Context, id int64) (*Map, error) {
	row, err := c.q.GetMap(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, row.Slug)
}

func (c *Catalog) load(ctx context.Context, slug string) (*Map, error) {
	row, err := c.q.GetMapBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m := &Map{
		ID:       row.ID,
		Slug:     row.Slug,
		Name:     places.NewText(row.NameUz, row.NameRu, row.NameEn),
		Desc:     places.NewText(row.DescUz, row.DescRu, row.DescEn),
		Official: row.OwnerID == nil,
		groups:   map[int64][]int{},
	}
	if err := json.Unmarshal(row.Rules, &m.Rules); err != nil {
		return nil, fmt.Errorf("map %s rules: %w", slug, err)
	}
	if err := json.Unmarshal(row.Pick, &m.Pick); err != nil {
		return nil, fmt.Errorf("map %s pick: %w", slug, err)
	}

	cands, err := c.q.MapCandidates(ctx, store.MapCandidatesParams{MapID: row.ID, StreetsOnly: m.Pick.StreetsOnly})
	if err != nil {
		return nil, err
	}
	var pbox geo.BBox
	streets := map[int64]bool{}
	for _, r := range cands {
		if _, ok := c.reg.Get(r.Provider); !ok {
			continue // provider not enabled on this server
		}
		cand := Candidate{Pano: provider.Pano{ID: r.ID, Provider: r.Provider, Ref: r.Ref, Pos: geo.Point{Lat: r.Lat, Lon: r.Lon}, Heading: r.Heading}}
		if r.StreetID != nil {
			cand.StreetID = *r.StreetID
			streets[cand.StreetID] = true
		}
		switch m.Pick.Group {
		case "street":
			cand.Group = cand.StreetID
		case "district":
			cand.Group = r.DistrictID
		case "region":
			cand.Group = r.RegionID
		}
		m.groups[cand.Group] = append(m.groups[cand.Group], len(m.Cands))
		m.Cands = append(m.Cands, cand)
		pbox = pbox.Extend(cand.Pos)
	}
	m.Streets = len(streets)

	ext, err := c.q.MapPlaceExtent(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	m.BBox = geo.BBox{ext.West, ext.South, ext.East, ext.North}
	if m.BBox == (geo.BBox{}) {
		m.BBox = pbox
	}
	m.SizeKm = m.BBox.Diagonal()
	outlines, err := c.q.MapOutlines(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if len(outlines) == 1 {
		m.Outline = outlines[0]
	}
	if m.Pick.StreetsOnly && pbox != (geo.BBox{}) {
		// a street map opens on its streets, not on the whole city
		const pad = 0.01
		m.BBox = geo.BBox{pbox[0] - pad, pbox[1] - pad, pbox[2] + pad, pbox[3] + pad}
	}
	return m, nil
}
