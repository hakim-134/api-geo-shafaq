package places

import "testing"

func TestProgress(t *testing.T) {
	country := &Place{ID: 1, Kind: "country", Panoramas: 60}
	city := &Place{ID: 2, Kind: "city", Parent: country, Panoramas: 50, Streets: 6}
	west := &Place{ID: 3, Kind: "district", Parent: city, Panoramas: 30, Streets: 2}
	east := &Place{ID: 4, Kind: "district", Parent: city, Panoramas: 20}
	region := &Place{ID: 5, Kind: "region", Parent: country, Panoramas: 10}
	country.Children = []*Place{city, region}
	city.Children = []*Place{west, east}

	for p, want := range map[*Place]string{country: UnitRegion, city: UnitStreet, west: UnitSpot, region: UnitSpot} {
		if got := p.Unit(); got != want {
			t.Errorf("place %d: unit %s, want %s", p.ID, got, want)
		}
	}
	if !city.RegionLevel() || west.RegionLevel() {
		t.Error("Tashkent counts as a region, its districts do not")
	}

	km := func(v float64) *float64 { return &v }
	rounds := []Round{
		{Panorama: 10, Places: []int64{1, 2, 3}, Street: 100, Km: km(0.2), GuessIn: []int64{1, 2, 3}},
		{Panorama: 11, Places: []int64{1, 2, 3}, Street: 100, Km: km(3)},
		{Panorama: 12, Places: []int64{1, 2, 4}, Street: 101, Km: km(2), GuessIn: []int64{1, 2, 3}},
		{Panorama: 13, Places: []int64{1, 5}, Km: nil},
	}
	check := func(name string, got Progress, total, seen, learned int) {
		t.Helper()
		if got.Total != total || got.Seen != seen || got.Learned != learned {
			t.Errorf("%s: %+v, want %d/%d/%d", name, got, total, seen, learned)
		}
	}
	check("city streets", city.Progress(rounds), 6, 2, 1)
	check("country regions", country.Progress(rounds), 2, 2, 1) // the region: seen, no guess
	check("west spots", west.Progress(rounds), 30, 2, 1)
	if p := west.Path(); len(p) != 2 || p[0] != country || p[1] != city {
		t.Errorf("path: %v", p)
	}
}
