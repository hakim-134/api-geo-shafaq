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

// Registry holds the enabled providers in two tiers. Players' providers (PROVIDERS, in order
// of preference) are what a game gets when its rules name none, and what the menu counts.
// Test providers (TEST_PROVIDERS) play only in games that ask for them by name: development
// and the smoke test use `demo` this way, and a new provider can be tried before players see
// it.
type Registry struct {
	list []Provider
	test []Provider
}

func NewRegistry(enabled, test []string, all ...Provider) (*Registry, error) {
	r := &Registry{}
	pick := func(names []string, into *[]Provider) error {
		for _, name := range names {
			i := slices.IndexFunc(all, func(p Provider) bool { return p.Name() == name })
			if i < 0 {
				return fmt.Errorf("unknown panorama provider %q", name)
			}
			if _, ok := r.Get(name); ok {
				return fmt.Errorf("panorama provider %q enabled twice", name)
			}
			*into = append(*into, all[i])
		}
		return nil
	}
	if err := pick(enabled, &r.list); err != nil {
		return nil, err
	}
	if err := pick(test, &r.test); err != nil {
		return nil, err
	}
	return r, nil
}

// Get finds an enabled provider of either tier.
func (r *Registry) Get(name string) (Provider, bool) {
	for _, p := range slices.Concat(r.list, r.test) {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Names are the players' providers.
func (r *Registry) Names() []string { return names(r.list) }

// Known are all enabled providers, test ones included: what a game's rules may name.
func (r *Registry) Known() []string { return names(slices.Concat(r.list, r.test)) }

func names(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

// Allowed is the set a game may use: the players' providers when the rules name none,
// otherwise the named ones that are enabled, test ones included.
func (r *Registry) Allowed(wanted []string) map[string]bool {
	out := map[string]bool{}
	if len(wanted) == 0 {
		for _, p := range r.list {
			out[p.Name()] = true
		}
		return out
	}
	for _, name := range wanted {
		if _, ok := r.Get(name); ok {
			out[name] = true
		}
	}
	return out
}
