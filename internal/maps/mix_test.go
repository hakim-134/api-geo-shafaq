package maps

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
)

// part is a map of n demo panoramas in a row from (lat, lon), 200 m apart, each on its own street.
func part(slug string, first int64, n int, lat, lon, sizeKm float64) *Map {
	m := &Map{Slug: slug, SizeKm: sizeKm, Pick: Pick{Group: "street", Pow: 0.5, SnapKm: 1, JitterKm: 0.1}}
	for i := range n {
		id := first + int64(i)
		m.add(Candidate{
			Pano:     provider.Pano{ID: id, Provider: "demo", Pos: geo.Point{Lat: lat + float64(i)*0.002, Lon: lon}},
			StreetID: id, Group: id, Hardness: float64(i%10) / 10,
		})
	}
	return m
}

func mixOf(parts ...*Map) *Map {
	m := &Map{Slug: "mix-test"}
	for _, p := range parts {
		m.Pick.Parts = append(m.Pick.Parts, p.Slug)
		m.addPart(p)
	}
	return m
}

var demoOnly = map[string]bool{"demo": true}

// play picks rounds as a game does (a seed, one rng per round) and says which part each came from.
func play(t *testing.T, m *Map, seed uint64, rounds int) []string {
	t.Helper()
	used := Used{IDs: map[int64]bool{}}
	var out []string
	for n := range rounds {
		c, ok := m.PickAt(rand.New(rand.NewPCG(seed, uint64(n))), demoOnly, used, rules.Normal)
		if !ok {
			t.Fatalf("seed %d: no panorama for round %d (%v so far)", seed, n+1, out)
		}
		if c.From == nil || !slices.Contains(m.Parts(), c.From) {
			t.Fatalf("a mix's round must say its part: %+v", c)
		}
		if used.IDs[c.ID] {
			t.Fatalf("seed %d: panorama %d twice", seed, c.ID)
		}
		used.IDs[c.ID] = true
		used.Pos = append(used.Pos, c.Pos)
		out = append(out, m.Source(c).Slug)
	}
	return out
}

// Tashkent's pool is 25 times Buxoro's: still every part comes in its turn.
func TestMixGivesEachPartItsTurn(t *testing.T) {
	m := mixOf(
		part("place-toshkent", 1, 500, 41.2, 69.2, 40),
		part("place-buxoro-viloyati", 1001, 20, 39.7, 64.4, 330),
		part("place-samarqand-viloyati", 2001, 60, 39.6, 66.9, 280),
	)
	orders := map[string]bool{}
	for seed := range uint64(50) {
		got := play(t, m, seed, 6)
		for i := 0; i < 6; i += 3 {
			turn := slices.Clone(got[i : i+3])
			slices.Sort(turn)
			if len(slices.Compact(turn)) != 3 {
				t.Fatalf("seed %d: a part came twice before every part came once: %v", seed, got)
			}
		}
		if again := play(t, m, seed, 6); !slices.Equal(got, again) {
			t.Fatalf("seed %d gave %v, then %v", seed, got, again)
		}
		orders[strings.Join(got, " ")] = true
	}
	if len(orders) < 3 {
		t.Fatalf("the parts' order hardly changes with the seed: %v", orders)
	}
}

func TestMixSkipsAPartWithNothingLeft(t *testing.T) {
	m := mixOf(
		part("place-a", 1, 50, 41.2, 69.2, 40),
		part("place-b", 101, 1, 39.7, 64.4, 300), // one panorama: one round
		part("place-c", 201, 50, 39.6, 66.9, 280),
	)
	got := play(t, m, 7, 9)
	count := map[string]int{}
	for _, s := range got {
		count[s]++
	}
	if count["place-b"] != 1 || count["place-a"] != 4 || count["place-c"] != 4 {
		t.Fatalf("b has one panorama, a and c share the rest: %v", got)
	}

	// a part with nothing on the game's providers is never its turn
	m = mixOf(part("place-a", 1, 50, 41.2, 69.2, 40), part("place-b", 101, 5, 39.7, 64.4, 300))
	for i := range m.Cands {
		if m.Cands[i].From.Slug == "place-b" {
			m.Cands[i].Provider = "yandex"
		}
	}
	for i := range m.parts[1].Cands {
		m.parts[1].Cands[i].Provider = "yandex"
	}
	for _, s := range play(t, m, 3, 4) {
		if s != "place-a" {
			t.Fatalf("picked from a part with no demo panoramas")
		}
	}
}

// A long street across two districts: shown in one part, it doesn't come again from the other.
func TestMixNeverTheSameStreetTwiceAcrossParts(t *testing.T) {
	a := part("place-a", 1, 5, 41.30, 69.2, 10)
	b := part("place-b", 101, 10, 41.32, 69.2, 10)
	for i := range a.Cands {
		a.Cands[i].StreetID = 7
	}
	for i := range 5 {
		b.Cands[i].StreetID = 7 // b's first five: the same street, other panoramas
	}
	m := mixOf(a, b)
	first := a.Cands[0]
	for seed := range uint64(100) {
		used := Used{IDs: map[int64]bool{first.ID: true}, Pos: []geo.Point{first.Pos}, Rounds: []int64{first.ID}}
		c, ok := m.PickAt(rand.New(rand.NewPCG(seed, 2)), demoOnly, used, rules.AnyPlace)
		if !ok || c.From != b {
			t.Fatalf("seed %d: round 2 is b's turn: %+v", seed, c)
		}
		if c.StreetID == 7 {
			t.Fatalf("seed %d: street 7 again, from the other part", seed)
		}
	}

	// If a part's remaining panoramas are all on the street already played, skip it.
	b = part("place-b", 101, 5, 41.32, 69.2, 10)
	for i := range b.Cands {
		b.Cands[i].StreetID = 7
	}
	fresh := part("place-c", 201, 5, 41.34, 69.2, 10)
	m = mixOf(a, b, fresh)
	if got := m.RoundCount(demoOnly); got != 6 {
		t.Fatalf("rounds available across the shared street and part c: %d, want 6", got)
	}
	c, ok := m.PickRound(rand.New(rand.NewPCG(1, 2)), demoOnly, Used{IDs: map[int64]bool{first.ID: true}})
	if !ok || c.From != fresh {
		t.Fatalf("a street already shown leaves only part c: %+v", c)
	}
}

// The mix keeps each part's own ways: its snap, jitter and size, and its box round all of them.
func TestMixKeepsEachPartsOwnWays(t *testing.T) {
	a := part("place-a", 1, 5, 41.30, 69.2, 10)
	a.Pick.JitterKm = 0
	a.BBox = geo.BBox{69.1, 41.2, 69.3, 41.4}
	b := part("place-b", 101, 5, 39.70, 64.4, 300)
	b.Pick.SnapKm, b.Pick.JitterKm = 0.12, 0.1
	b.BBox = geo.BBox{63, 39, 66, 41}
	m := mixOf(a, b)
	if m.BBox != (geo.BBox{63, 39, 69.3, 41.4}) || m.SizeKm != m.BBox.Diagonal() {
		t.Fatalf("the box of a mix holds every part: %v %.0f km", m.BBox, m.SizeKm)
	}
	if !m.Mix() || m.Metric() != "mix" || a.Metric() != "place-a" {
		t.Fatal("every mix counts as one on the metrics")
	}
	rng := rand.New(rand.NewPCG(1, 1))
	for range 50 {
		c, _ := m.PickRound(rng, demoOnly, Used{})
		src := m.Source(c)
		near := m.Near(rng, c)
		switch src {
		case a:
			if near != c.Pos {
				t.Fatalf("a has no jitter: %v moved to %v", c.Pos, near)
			}
		case b:
			if geo.DistanceKm(near, c.Pos) > 0.1001 {
				t.Fatalf("b's jitter is 100 m: %.3f km", geo.DistanceKm(near, c.Pos))
			}
		default:
			t.Fatalf("no part: %+v", c)
		}
	}
	if a.Source(a.Cands[0]) != a {
		t.Fatal("a map's own panorama comes from the map itself")
	}
}

func TestMixSlug(t *testing.T) {
	x := MixSlug([]string{"place-yunusobod-tumani", "place-buxoro-viloyati", "place-samarqand-viloyati"})
	y := MixSlug([]string{"place-samarqand-viloyati", "place-yunusobod-tumani", "place-buxoro-viloyati"})
	if x != y || !strings.HasPrefix(x, MixPrefix) || len(x) != len(MixPrefix)+12 {
		t.Fatalf("the same parts in any order are one mix: %s %s", x, y)
	}
	if x == MixSlug([]string{"place-yunusobod-tumani", "place-buxoro-viloyati"}) {
		t.Fatal("other parts, the same slug")
	}
}

// A panorama that failed to open was never seen: the round's replacement may stand on its street.
func TestMixReplacementMayUseTheFailedPanoramasStreet(t *testing.T) {
	a := part("place-a", 1, 3, 41.30, 69.2, 10)
	b := part("place-b", 101, 3, 41.32, 69.2, 10)
	for i := range b.Cands {
		b.Cands[i].StreetID = 9
	}
	m := mixOf(a, b)
	played, failed := a.Cands[0], b.Cands[0]
	for seed := range uint64(50) {
		used := Used{
			IDs:    map[int64]bool{played.ID: true, failed.ID: true},
			Pos:    []geo.Point{played.Pos},
			Rounds: []int64{played.ID}, // round 1 played a; round 2's b panorama failed
		}
		c, ok := m.PickAt(rand.New(rand.NewPCG(seed, 3)), demoOnly, used, rules.AnyPlace)
		if !ok || c.From != b || c.ID == failed.ID {
			t.Fatalf("seed %d: round 2's replacement is another of b's on street 9: %+v ok=%v", seed, c, ok)
		}
	}
}
