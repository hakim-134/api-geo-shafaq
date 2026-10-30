package maps

import (
	"math"
	"math/rand/v2"
	"slices"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
)

// Used is what earlier rounds of a game already showed.
type Used struct {
	IDs map[int64]bool
	Pos []geo.Point
}

// PickRound picks a panorama for a round: a group first (weighted by its size to the power
// Pick.Pow), then a panorama in it. It avoids panoramas the game already showed, their
// streets (a long street like Kichik halqa yo‘li came twice in one game), and places too close
// to them (3% of the map's size: 1.2 km in Tashkent); after 40 tries it settles for anything
// not shown yet. ok is false when the allowed providers have nothing here.
func (m *Map) PickRound(rng *rand.Rand, allowed map[string]bool, used Used) (c Candidate, ok bool) {
	type group struct {
		idx    []int
		weight float64
	}
	var groups []group
	total := 0.0
	for _, idx := range m.groups {
		var in []int
		for _, i := range idx {
			if allowed[m.Cands[i].Provider] && !used.IDs[m.Cands[i].ID] {
				in = append(in, i)
			}
		}
		if len(in) == 0 {
			continue
		}
		w := math.Pow(float64(len(in)), m.Pick.Pow)
		groups = append(groups, group{in, w})
		total += w
	}
	if len(groups) == 0 {
		return Candidate{}, false
	}
	// map iteration order is random; sort so a seed always gives the same game
	slices.SortFunc(groups, func(a, b group) int { return a.idx[0] - b.idx[0] })

	streets := map[int64]bool{}
	for _, c := range m.Cands {
		if c.StreetID != 0 && used.IDs[c.ID] {
			streets[c.StreetID] = true
		}
	}

	minGap := m.SizeKm * 0.03
	var last Candidate
	for try := 0; try < 40; try++ {
		r := rng.Float64() * total
		g := groups[len(groups)-1]
		for _, gg := range groups {
			if r -= gg.weight; r <= 0 {
				g = gg
				break
			}
		}
		last = m.Cands[g.idx[rng.IntN(len(g.idx))]]
		if far(last.Pos, used.Pos, minGap) && !streets[last.StreetID] {
			return last, true
		}
	}
	return last, true
}

func far(p geo.Point, others []geo.Point, km float64) bool {
	for _, o := range others {
		if geo.DistanceKm(p, o) < km {
			return false
		}
	}
	return true
}

// Near is where the viewer looks for the panorama: the library point, moved up to JitterKm
// for variety on maps whose points are sparse.
func (m *Map) Near(rng *rand.Rand, c Candidate) geo.Point {
	if m.Pick.JitterKm <= 0 {
		return c.Pos
	}
	return geo.Offset(c.Pos, rng.Float64()*m.Pick.JitterKm, rng.Float64()*2*math.Pi)
}
