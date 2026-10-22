// Package geo has the little geometry the game needs outside PostGIS: distances, boxes and
// points around a centre.
package geo

import "math"

const earthKm = 6371.0

// Point is a position in degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Valid reports whether the point is a real position on Earth.
func (p Point) Valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lon) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lon >= -180 && p.Lon <= 180
}

func rad(d float64) float64 { return d * math.Pi / 180 }

// DistanceKm is the great-circle (haversine) distance.
func DistanceKm(a, b Point) float64 {
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Pow(math.Sin(dLon/2), 2)
	return earthKm * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// Offset moves p by km in the direction bearing (radians, 0 = north). Good enough for the
// few kilometres of a hint circle.
func Offset(p Point, km, bearing float64) Point {
	return Point{
		Lat: p.Lat + km/111.32*math.Cos(bearing),
		Lon: p.Lon + km/(111.32*math.Cos(rad(p.Lat)))*math.Sin(bearing),
	}
}

// BBox is [west, south, east, north].
type BBox [4]float64

// Diagonal is the box's diagonal in km: a map's size D for scoring.
func (b BBox) Diagonal() float64 {
	return DistanceKm(Point{Lat: b[1], Lon: b[0]}, Point{Lat: b[3], Lon: b[2]})
}

// Extend grows the box to hold p. The zero BBox is empty.
func (b BBox) Extend(p Point) BBox {
	if b == (BBox{}) {
		return BBox{p.Lon, p.Lat, p.Lon, p.Lat}
	}
	return BBox{min(b[0], p.Lon), min(b[1], p.Lat), max(b[2], p.Lon), max(b[3], p.Lat)}
}
