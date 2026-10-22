// Package score turns a distance into points. It is the only place the formula lives: the
// game only shows what the server sends.
package score

import "math"

// Max is the score of a perfect round.
const Max = 5000

// Round scores a guess km away from the real place on a map of size sizeKm (the map's
// bounding-box diagonal, D). scale stretches D: 2 is forgiving, 0.6 is strict.
//
//	score = 5000 · e^(−10·d / (D·scale))
//
// A guess within D/1000 is perfect. On Tashkent (D ≈ 40 km) that is 40 m, and 4 km away
// gives 5000·e⁻¹ ≈ 1839, the same as v0's scaleKm = 4.
func Round(km, sizeKm, scale float64) int {
	if km < 0 || math.IsNaN(km) {
		return 0
	}
	d := sizeKm * scale
	if d <= 0 {
		return 0
	}
	if km <= d/1000 {
		return Max
	}
	return int(math.Round(Max * math.Exp(-10*km/d)))
}

// WithHints takes each hint's share off the score: two hints at 0.2 keep 64%.
func WithHints(score, hints int, cost float64) int {
	if hints <= 0 || cost <= 0 {
		return score
	}
	return int(math.Round(float64(score) * math.Pow(1-cost, float64(hints))))
}
