// Package provider is the seam between the game and the panorama sources. The game only
// ever sees this interface; adding a source is one adapter here, one in the web app and a
// pipeline run.
package provider

import (
	"fmt"
	"slices"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
)

// Capabilities tell the game what a provider can do, so rules offer only what works.
type Capabilities struct {
	Move        bool   // the player can walk to other panoramas
	HidesCoords bool   // the browser never learns the position (cheat-proof)
	HasDate     bool   // when the panorama was taken
	SearchNear  bool   // the viewer searches near a point and reports what it opened
	Credit      string // shown under the panorama
}

// Pano is a panorama from the library.
type Pano struct {
	ID       int64
	Provider string
	Ref      string
	Pos      geo.Point
	Heading  *float32
}

// View is what the browser's viewer gets for a round, and nothing more.
type View struct {
	Provider string
	Near     *geo.Point // SearchNear providers: where to look for the panorama
	SnapKm   float64    // and how far from Near the opened one may be
	Ref      string     // the provider's id, or an opaque reference
	ImageURL string     // an image the viewer opens directly
	Heading  float64    // where to look first, degrees from north
}

// Provider is one panorama source.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	// View builds the round's payload. near is the point to search around (the library
	// position, maybe moved a little); snapKm is the map's tolerance for SearchNear providers.
	View(p Pano, near geo.Point, snapKm, heading float64) View
}

// Registry holds the enabled providers in order of preference.
type Registry struct {
	list []Provider
}

func NewRegistry(enabled []string, all ...Provider) (*Registry, error) {
	r := &Registry{}
	for _, name := range enabled {
		i := slices.IndexFunc(all, func(p Provider) bool { return p.Name() == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown panorama provider %q", name)
		}
		r.list = append(r.list, all[i])
	}
	return r, nil
}

func (r *Registry) Get(name string) (Provider, bool) {
	for _, p := range r.list {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Names are the enabled providers.
func (r *Registry) Names() []string {
	out := make([]string, len(r.list))
	for i, p := range r.list {
		out[i] = p.Name()
	}
	return out
}

// Allowed is the set a game may use: the rule's list (empty = all), within what is enabled.
func (r *Registry) Allowed(wanted []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range r.list {
		if len(wanted) == 0 || slices.Contains(wanted, p.Name()) {
			out[p.Name()] = true
		}
	}
	return out
}
