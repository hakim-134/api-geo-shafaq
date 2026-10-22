// Package mapillary is provider #2: free, CC-BY-SA street imagery, viewed with MapillaryJS
// in the browser. Its coverage in Uzbekistan is thin; Audit counts it.
package mapillary

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
)

// Provider is the Mapillary adapter. The browser opens an image by its id; MapillaryJS
// loads the rest (and the position) from Mapillary itself.
type Provider struct{}

func (Provider) Name() string { return "mapillary" }

func (Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Move: true, HasDate: true, Credit: "© Mapillary contributors, CC BY-SA"}
}

func (Provider) View(p provider.Pano, _ geo.Point, _ float64, heading float64) provider.View {
	return provider.View{Provider: "mapillary", Ref: p.Ref, Heading: heading}
}

// The audit looks at the main cities: Tashkent in full, the others around their centres.
var auditAreas = []struct {
	Name string
	BBox geo.BBox
}{
	{"Toshkent", geo.BBox{69.10, 41.20, 69.45, 41.42}},
	{"Samarqand", geo.BBox{66.88, 39.60, 67.05, 39.70}},
	{"Buxoro", geo.BBox{64.36, 39.73, 64.48, 39.80}},
	{"Xiva / Urganch", geo.BBox{60.32, 41.35, 60.68, 41.58}},
	{"Namangan", geo.BBox{71.58, 40.96, 71.72, 41.04}},
	{"Andijon", geo.BBox{72.28, 40.74, 72.40, 40.81}},
	{"Farg‘ona", geo.BBox{71.72, 40.35, 71.83, 40.42}},
	{"Nukus", geo.BBox{59.55, 42.43, 59.66, 42.49}},
}

const tileURL = "https://tiles.mapillary.com/maps/vtp/mly1_public/2/%d/%d/%d?access_token=%s"

// Audit counts Mapillary images and 360° panoramas in the main cities, from Mapillary's
// public vector tiles at zoom 14 (one point per image). It needs a token: create a free
// app at mapillary.com/dashboard/developers and put its client token in MAPILLARY_TOKEN.
func Audit(ctx context.Context, token string, out io.Writer) error {
	if token == "" {
		return errors.New("MAPILLARY_TOKEN is empty: create a free app at https://www.mapillary.com/dashboard/developers")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	fmt.Fprintf(out, "%-16s %8s %8s  %s\n", "area", "images", "360°", "newest 360°")
	for _, a := range auditAreas {
		images, panos, newest, err := countArea(ctx, client, token, a.BBox)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		date := "-"
		if newest > 0 {
			date = time.UnixMilli(newest).Format("2006-01-02")
		}
		fmt.Fprintf(out, "%-16s %8d %8d  %s\n", a.Name, images, panos, date)
	}
	return nil
}

func countArea(ctx context.Context, client *http.Client, token string, b geo.BBox) (images, panos int, newest int64, err error) {
	const z = maptile.Zoom(14)
	min := maptile.At(orb.Point{b[0], b[3]}, z)
	max := maptile.At(orb.Point{b[2], b[1]}, z)
	seen := map[any]bool{}
	for x := min.X; x <= max.X; x++ {
		for y := min.Y; y <= max.Y; y++ {
			layers, err := fetchTile(ctx, client, fmt.Sprintf(tileURL, z, x, y, token))
			if err != nil {
				return 0, 0, 0, err
			}
			for _, l := range layers {
				if l.Name != "image" {
					continue
				}
				for _, f := range l.Features {
					if seen[f.ID] {
						continue
					}
					seen[f.ID] = true
					images++
					if pano, _ := f.Properties["is_pano"].(bool); pano {
						panos++
						if t, ok := f.Properties["captured_at"].(float64); ok {
							newest = int64(math.Max(float64(newest), t))
						}
					}
				}
			}
		}
	}
	return images, panos, newest, nil
}

func fetchTile(ctx context.Context, client *http.Client, url string) (mvt.Layers, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusNoContent:
		return nil, nil // no imagery in this tile
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("tile: %s", res.Status)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	return mvt.Unmarshal(body)
}
