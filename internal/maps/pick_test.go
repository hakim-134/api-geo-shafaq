package maps

import (
	"math/rand/v2"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
)

// testMap: a big group of 100 panoramas on one street and a small one of 4, 10 km apart.
func testMap(pow float64) *Map {
	m := &Map{Pick: Pick{Pow: pow}, SizeKm: 40, groups: map[int64][]int{}}
	add := func(group int64, prov string, lat, lon float64) {
		m.groups[group] = append(m.groups[group], len(m.Cands))
		m.Cands = append(m.Cands, Candidate{
			Pano:  provider.Pano{ID: int64(len(m.Cands) + 1), Provider: prov, Pos: geo.Point{Lat: lat, Lon: lon}},
			Group: group,
		})
	}
	for i := range 100 {
		add(1, "yandex", 41.3+float64(i)*0.001, 69.2)
	}
	for i := range 4 {
		add(2, "demo", 41.4+float64(i)*0.001, 69.3)
	}
	return m
}

func TestPickSameSeedSameGame(t *testing.T) {
	m := testMap(0.5)
	all := map[string]bool{"yandex": true, "demo": true}
	a, _ := m.PickRound(rand.New(rand.NewPCG(7, 1)), all, Used{})
	b, _ := m.PickRound(rand.New(rand.NewPCG(7, 1)), all, Used{})
	if a.ID != b.ID {
		t.Fatalf("same seed picked %d and %d", a.ID, b.ID)
	}
}

func TestPickWeighting(t *testing.T) {
	all := map[string]bool{"yandex": true, "demo": true}
	small := func(pow float64) float64 {
		m := testMap(pow)
		rng := rand.New(rand.NewPCG(1, 2))
		n := 0
		for range 4000 {
			if c, _ := m.PickRound(rng, all, Used{}); c.Group == 2 {
				n++
			}
		}
		return float64(n) / 4000
	}
	// pow 0: groups equal (50%); pow 0.5: sqrt(4)/(sqrt(4)+sqrt(100)) = 1/6; pow 1: 4/104
	for _, c := range []struct{ pow, want float64 }{{0, 0.5}, {0.5, 1.0 / 6}, {1, 4.0 / 104}} {
		if got := small(c.pow); got < c.want-0.03 || got > c.want+0.03 {
			t.Errorf("pow %v: small group picked %.3f of the time, want about %.3f", c.pow, got, c.want)
		}
	}
}

func TestPickAvoidsRepeatsAndProviders(t *testing.T) {
	m := testMap(0.5)
	onlyDemo := map[string]bool{"demo": true}
	used := Used{IDs: map[int64]bool{}}
	rng := rand.New(rand.NewPCG(3, 3))
	for range 4 {
		c, ok := m.PickRound(rng, onlyDemo, used)
		if !ok || c.Provider != "demo" || used.IDs[c.ID] {
			t.Fatalf("picked %+v (ok=%v)", c, ok)
		}
		used.IDs[c.ID] = true
		used.Pos = append(used.Pos, c.Pos)
	}
	if _, ok := m.PickRound(rng, onlyDemo, used); ok {
		t.Fatal("all four demo panoramas were shown, yet a fifth was picked")
	}
	if _, ok := m.PickRound(rng, map[string]bool{"google": true}, Used{}); ok {
		t.Fatal("picked from a provider with no panoramas here")
	}
}

func TestPickKeepsDistance(t *testing.T) {
	m := testMap(0) // groups equal, so the far group comes up often
	all := map[string]bool{"yandex": true, "demo": true}
	first := m.Cands[0]
	used := Used{IDs: map[int64]bool{first.ID: true}, Pos: []geo.Point{first.Pos}}
	rng := rand.New(rand.NewPCG(9, 9))
	for range 50 {
		c, _ := m.PickRound(rng, all, used)
		if geo.DistanceKm(c.Pos, first.Pos) < m.SizeKm*0.03 && c.Group == 1 {
			// allowed only when 40 tries found nothing far, which cannot happen here
			t.Fatalf("picked %v, %.2f km from an earlier round", c.Pos, geo.DistanceKm(c.Pos, first.Pos))
		}
	}
}

func TestPickAvoidsShownStreets(t *testing.T) {
	m := testMap(0)
	for i := range m.Cands {
		m.Cands[i].StreetID = m.Cands[i].Group // group 1 is one long street, group 2 another
	}
	all := map[string]bool{"yandex": true, "demo": true}
	first := m.Cands[0]
	used := Used{IDs: map[int64]bool{first.ID: true}} // no positions: only the street keeps it away
	rng := rand.New(rand.NewPCG(5, 5))
	for range 50 {
		if c, _ := m.PickRound(rng, all, used); c.StreetID == first.StreetID {
			t.Fatalf("picked street %d again, with another street to show", c.StreetID)
		}
	}
}
