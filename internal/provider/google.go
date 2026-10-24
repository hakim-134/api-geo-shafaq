package provider

import "github.com/hakim-134/api-geo-shafaq/internal/geo"

// Google: provider #2 (decision 0008). The library keeps Google's pano IDs (its terms allow
// storing them); the browser opens one with the Maps JS API's StreetViewPanorama. Google's
// terms forbid Street View next to a non-Google map, so a Google round brings a Google
// guess map too. The position reaches the browser, as with Yandex.
type Google struct{}

func (Google) Name() string { return "google" }

func (Google) Capabilities() Capabilities {
	return Capabilities{Move: true, HasDate: true, Credit: "© Google"}
}

func (Google) View(p Pano, _ geo.Point, _, heading float64) View {
	return View{Provider: "google", Ref: p.Ref, Heading: heading}
}
