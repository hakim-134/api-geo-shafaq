// Package data holds seed data that ships inside the binaries.
//
//	v0/locations.json  680 exact Yandex panorama positions all over Uzbekistan (v0's pool)
//	v0/tashkent.json   185 main Tashkent streets with 2 356 points, and 49 metro stations
//	                   (© OpenStreetMap contributors, ODbL)
package data

import "embed"

//go:embed v0/*.json
var V0 embed.FS
