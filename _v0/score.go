package main

import "math"

// Keep in sync with AREAS and DIFFICULTIES in js/data.js.
var areaScaleKm = map[string]float64{
	"tashkent":         4,
	"tashkent_streets": 4,
	"historic":         80,
	"uzbekistan":       150,
}

type difficulty struct {
	scoreMul float64
	hints    bool
	hintCost float64
}

var difficulties = map[string]difficulty{
	"easy":   {scoreMul: 2, hints: true},
	"normal": {scoreMul: 1, hints: true, hintCost: 0.2},
	"hard":   {scoreMul: 0.6},
}

const maxRoundScore = 5000

// LatLon is [lat, lon], the same order the browser uses.
type LatLon [2]float64

// insideBox is a cheap sanity check; the browser does the exact border check.
func (p LatLon) insideBox() bool {
	return p[0] > 37 && p[0] < 46 && p[1] > 55.5 && p[1] < 73.5
}

func distanceKm(a, b LatLon) float64 {
	const r = 6371
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(b[0] - a[0])
	dLon := rad(b[1] - a[1])
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(a[0]))*math.Cos(rad(b[0]))*math.Pow(math.Sin(dLon/2), 2)
	return r * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// roundScore mirrors calcScore in js/game.js.
func roundScore(s Settings, km float64, hint bool) int {
	d := difficulties[s.Diff]
	scale := areaScaleKm[s.Area] * d.scoreMul
	score := maxRoundScore
	if km > scale*0.01 {
		score = int(math.Round(maxRoundScore * math.Exp(-km/scale)))
	}
	if hint && d.hints && d.hintCost > 0 {
		score = int(math.Round(float64(score) * (1 - d.hintCost)))
	}
	return score
}
