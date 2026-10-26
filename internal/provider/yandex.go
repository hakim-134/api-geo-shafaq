package provider

import "github.com/hakim-134/api-geo-shafaq/internal/geo"

// Yandex: the main coverage in Uzbekistan today. The JS API 2.1 finds a panorama near a
// point (ymaps.panorama.locate); the browser reports which one it opened and the server
// checks it is close to the point it chose. The position reaches the browser, so a player
// with DevTools can cheat; it is fine for casual games, not for ranked ones.
//
// Free terms: about 100 panorama views a day, no paid products, data cached at most 30
// days. Views are counted per day (see Usage) and the license is bought before the limit.
type Yandex struct{}

func (Yandex) Name() string { return "yandex" }

func (Yandex) Capabilities() Capabilities {
	return Capabilities{Move: true, SearchNear: true, Credit: "© Yandex", GuessMap: "yandex"}
}

func (Yandex) View(p Pano, near geo.Point, snapKm, heading float64) View {
	return View{Provider: "yandex", Near: &near, SnapKm: snapKm, Heading: heading, GuessMap: "yandex"}
}
