package places

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// LearnedKm: a street or a spot counts as learned once a guess on it lands this close.
const LearnedKm = 0.5

// A place is about its streets once it has this many with panoramas (Tashkent and its
// districts); otherwise about the places inside it, or its single spots.
const minStreets = 5

// Units progress is counted in.
const (
	UnitStreet   = "street"   // named streets
	UnitDistrict = "district" // the districts and towns inside
	UnitRegion   = "region"   // the regions (and Tashkent) inside the country
	UnitSpot     = "spot"     // single panoramas
)

// Place is one node of the place tree, with how much there is to play in it.
type Place struct {
	ID, ParentID int64
	Kind, Slug   string
	Name         Text
	SizeKm       float64
	Outline      string
	Banner       string
	BannerCredit string
	Panoramas    int // active, on the players' providers
	Streets      int // named streets with such a panorama
	Parent       *Place
	Children     []*Place // largest offer first
}

// RegionLevel: a region, or a city that is one (Tashkent).
func (p *Place) RegionLevel() bool {
	return p.Kind == "region" || p.Kind == "city" && p.Parent != nil && p.Parent.Kind == "country"
}

// Playable children: those with panoramas.
func (p *Place) Playable() []*Place {
	var out []*Place
	for _, c := range p.Children {
		if c.Panoramas > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Unit is what progress in this place counts, and what its map groups rounds by.
func (p *Place) Unit() string {
	switch {
	case p.Streets >= minStreets && p.Kind != "country":
		return UnitStreet
	case len(p.Playable()) >= 2 && p.Kind == "country":
		return UnitRegion
	case len(p.Playable()) >= 2:
		return UnitDistrict
	}
	return UnitSpot
}

// Path is the place's ancestors, the country first.
func (p *Place) Path() []*Place {
	var out []*Place
	for q := p.Parent; q != nil; q = q.Parent {
		out = append(out, q)
	}
	slices.Reverse(out)
	return out
}

// Tree is every place, by slug and by id.
type Tree struct {
	bySlug map[string]*Place
	byID   map[int64]*Place
	Root   *Place
}

func (t *Tree) Get(slug string) *Place { return t.bySlug[slug] }
func (t *Tree) ByID(id int64) *Place   { return t.byID[id] }

// LoadTree reads the places and counts their panoramas on the given providers.
func LoadTree(ctx context.Context, q *store.Queries, providers []string) (*Tree, error) {
	rows, err := q.ListPlaces(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := q.PlaceCounts(ctx, providers)
	if err != nil {
		return nil, err
	}
	t := &Tree{bySlug: map[string]*Place{}, byID: map[int64]*Place{}}
	for _, r := range rows {
		p := &Place{
			ID: r.ID, ParentID: r.ParentID, Kind: r.Kind, Slug: r.Slug,
			Name: NewText(r.NameUz, r.NameRu, r.NameEn), SizeKm: r.SizeKm, Outline: r.OutlineSvg,
			Banner: r.BannerUrl, BannerCredit: r.BannerCredit,
		}
		t.bySlug[p.Slug], t.byID[p.ID] = p, p
	}
	for _, c := range counts {
		if p := t.byID[c.PlaceID]; p != nil {
			p.Panoramas, p.Streets = int(c.Panoramas), int(c.Streets)
		}
	}
	for _, p := range t.byID {
		if p.Parent = t.byID[p.ParentID]; p.Parent != nil {
			p.Parent.Children = append(p.Parent.Children, p)
		} else if p.Kind == "country" {
			t.Root = p
		}
	}
	for _, p := range t.byID {
		slices.SortFunc(p.Children, func(a, b *Place) int {
			if a.Panoramas != b.Panoramas {
				return b.Panoramas - a.Panoramas
			}
			return strings.Compare(a.Name.Uz, b.Name.Uz)
		})
	}
	return t, nil
}

// Round is one round the player guessed, as progress needs it.
type Round struct {
	Panorama int64
	Places   []int64 // the panorama's places
	Street   int64
	Km       *float64
	GuessIn  []int64 // the places the guess landed in
}

func UserRounds(ctx context.Context, q *store.Queries, userID uuid.UUID) ([]Round, error) {
	rows, err := q.UserRoundPlaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Round, len(rows))
	for i, r := range rows {
		out[i] = Round{Panorama: r.PanoramaID, Places: r.PlaceIds, Street: r.StreetID, Km: r.Km, GuessIn: r.GuessPlaces}
	}
	return out, nil
}

// Progress is how much of a place the player has seen (played a round there) and learned:
// a street or spot guessed within LearnedKm, a district or region guessed inside it.
type Progress struct {
	Unit                 string
	Total, Seen, Learned int
}

func (p *Place) Progress(rounds []Round) Progress {
	out := Progress{Unit: p.Unit()}
	seen, learned := map[int64]bool{}, map[int64]bool{}
	close := func(r Round) bool { return r.Km != nil && *r.Km <= LearnedKm }
	children := p.Playable()
	for _, r := range rounds {
		if !slices.Contains(r.Places, p.ID) {
			continue
		}
		switch out.Unit {
		case UnitStreet:
			if r.Street != 0 {
				seen[r.Street] = true
				learned[r.Street] = learned[r.Street] || close(r)
			}
		case UnitSpot:
			seen[r.Panorama] = true
			learned[r.Panorama] = learned[r.Panorama] || close(r)
		default:
			for _, c := range children {
				if slices.Contains(r.Places, c.ID) {
					seen[c.ID] = true
					learned[c.ID] = learned[c.ID] || slices.Contains(r.GuessIn, c.ID)
				}
			}
		}
	}
	switch out.Unit {
	case UnitStreet:
		out.Total = p.Streets
	case UnitSpot:
		out.Total = p.Panoramas
	default:
		out.Total = len(children)
	}
	for _, l := range learned {
		if l {
			out.Learned++
		}
	}
	// test panoramas (demo twins) are other rows on the same spots: never more than there is
	out.Seen, out.Learned = min(len(seen), out.Total), min(out.Learned, out.Total)
	return out
}

// Cache keeps the tree in memory, reloading it a few minutes after it was read.
type Cache struct {
	Q         *store.Queries
	Providers []string // the players' providers: what is counted
	TTL       time.Duration

	mu     sync.Mutex
	tree   *Tree
	loaded time.Time
}

func (c *Cache) Get(ctx context.Context) (*Tree, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if c.tree != nil && time.Since(c.loaded) < ttl {
		return c.tree, nil
	}
	t, err := LoadTree(ctx, c.Q, c.Providers)
	if err != nil {
		return nil, err
	}
	c.tree, c.loaded = t, time.Now()
	return t, nil
}
