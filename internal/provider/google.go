package provider

import "github.com/hakim-134/api-geo-shafaq/internal/geo"

// Google: provider #2 (decision 0008). The library keeps Google's pano IDs (its terms allow
// storing them); the browser opens one with the Maps JS API's StreetViewPanorama. Google's
// terms forbid Street View next to a non-Google map, so a Google round says GuessMap
// "google" and the web guesses on a Google map. The position reaches the browser, as with
// Yandex.
//
// Price: 5 000 free panorama loads a month, then $14 per 1 000 (Dynamic Street View), so
// its default daily cap (DAILY_CAPS, google=160) keeps it inside the free tier.
type Google struct{}

func (Google) Name() string { return "google" }

func (Google) Capabilities() Capabilities {
	return Capabilities{Move: true, HasDate: true, Credit: "© Google", GuessMap: "google"}
}

func (Google) View(p Pano, _ geo.Point, _, heading float64) View {
	return View{Provider: "google", Ref: p.Ref, Heading: heading, GuessMap: "google"}
}
