package maps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
	"github.com/hakim-134/api-geo-shafaq/internal/testdb"
)

// mixCatalog: a country with Tashkent (and Yunusobod in it), Buxoro and Samarqand, four demo
// panoramas in each but the country, and the map of all Uzbekistan.
func mixCatalog(t *testing.T) *Catalog {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	place := func(kind, slug, name, parent string, panoramas int, w, s, e, n float64) {
		env := fmt.Sprintf("ST_Multi(ST_MakeEnvelope(%f, %f, %f, %f, 4326))", w, s, e, n)
		exec(fmt.Sprintf(`INSERT INTO places (kind, slug, name_uz, geom, geom_simple, size_km, parent_id)
			VALUES ($1, $2, $3, %s, %s, ST_DistanceSphere(ST_MakePoint(%f, %f), ST_MakePoint(%f, %f)) / 1000,
			        (SELECT id FROM places WHERE slug = $4))`, env, env, w, s, e, n), kind, slug, name, parent)
		for i := range panoramas {
			lat, lon := s+(n-s)*(0.2+0.2*float64(i)), w+(e-w)*0.5
			exec(`INSERT INTO panoramas (provider, ref, pos, source) VALUES ('demo', $1, ST_SetSRID(ST_MakePoint($3, $2), 4326), 'test')`,
				fmt.Sprintf("%s-%d", slug, i), lat, lon)
		}
	}
	place("country", "uzbekistan", "O‘zbekiston", "", 0, 56, 37, 73, 46)
	place("city", "toshkent", "Toshkent", "uzbekistan", 4, 69.1, 41.2, 69.4, 41.4)
	place("district", "yunusobod-tumani", "Yunusobod tumani", "toshkent", 4, 69.25, 41.33, 69.35, 41.4)
	place("region", "buxoro-viloyati", "Buxoro viloyati", "uzbekistan", 4, 62, 39, 65, 41)
	place("region", "samarqand-viloyati", "Samarqand viloyati", "uzbekistan", 4, 65.5, 39, 67.5, 40.5)
	exec(`UPDATE panoramas p SET place_ids = (SELECT array_agg(pl.id ORDER BY pl.size_km DESC) FROM places pl WHERE ST_Contains(pl.geom, p.pos))`)
	exec(`INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('uzbekistan', 'Butun O‘zbekiston', '{}', '{"group":"region","pow":0.25,"snapKm":1}')`)
	exec(`INSERT INTO map_sources (map_id, place_id) SELECT m.id, p.id FROM maps m, places p WHERE m.slug = 'uzbekistan' AND p.slug = 'uzbekistan'`)
	exec(`INSERT INTO maps (slug, name_uz, rules, pick) VALUES ('tashkent-streets', 'Toshkent ko‘chalari', '{}', '{"streetsOnly":true,"group":"street","snapKm":0.12}')`)
	exec(`INSERT INTO map_sources (map_id, place_id) SELECT m.id, p.id FROM maps m, places p WHERE m.slug = 'tashkent-streets' AND p.slug = 'toshkent'`)
	reg, err := provider.NewRegistry([]string{"yandex"}, []string{"demo"}, provider.Yandex{}, provider.Demo{Secret: []byte("t")})
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	return NewCatalog(pool, reg, &places.Cache{Q: q, Providers: reg.Names()})
}

func TestMixMakesOneMapOfItsParts(t *testing.T) {
	c := mixCatalog(t)
	ctx := context.Background()

	m, err := c.Mix(ctx, []string{"place-yunusobod-tumani", "place-buxoro-viloyati", "place-samarqand-viloyati"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Mix() || m.Slug != MixSlug([]string{"place-buxoro-viloyati", "place-samarqand-viloyati", "place-yunusobod-tumani"}) {
		t.Fatalf("a mix of three places: %s %v", m.Slug, m.Pick)
	}
	if m.Name.Uz != "Yunusobod tumani, Buxoro viloyati, Samarqand viloyati" || len(m.Parts()) != 3 || m.Parts()[0].Slug != "place-yunusobod-tumani" {
		t.Fatalf("named and ordered as first given: %q %v", m.Name.Uz, m.Pick.Parts)
	}
	if len(m.Cands) != 12 || len(m.Places) != 3 || m.Face != "uzbekistan" || m.Outline != "" {
		t.Fatalf("every part's panoramas and places, the face round them: %d %v %q", len(m.Cands), m.Places, m.Face)
	}
	if m.BBox[0] != 62 || m.BBox[2] != 69.35 || m.SizeKm < 500 {
		t.Fatalf("the box holds every part: %v (%.0f km)", m.BBox, m.SizeKm)
	}
	for _, p := range m.Parts() {
		if p.SizeKm >= m.SizeKm {
			t.Fatalf("each part keeps its own size: %s %.0f", p.Slug, p.SizeKm)
		}
	}

	// the same parts in another order: the same map, made once
	again, err := c.Mix(ctx, []string{"place-samarqand-viloyati", "place-yunusobod-tumani", "place-buxoro-viloyati", "place-samarqand-viloyati"})
	if err != nil || again.ID != m.ID {
		t.Fatalf("the same parts gave another map: %v %v", again, err)
	}
	// never on the menu
	list, err := c.Official(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range list {
		if o.Mix() {
			t.Fatalf("a mix on the menu: %s", o.Slug)
		}
	}
	// a list of games finds its face without loading it
	fresh := NewCatalog(c.pool, c.reg, c.tree)
	faces, err := fresh.Faces(ctx, []string{m.Slug})
	if err != nil || faces[m.Slug] != "uzbekistan" {
		t.Fatalf("the mix's face from its places: %v %v", faces, err)
	}
	// and a new process plays it by its slug
	loaded, err := fresh.Get(ctx, m.Slug)
	if err != nil || len(loaded.Parts()) != 3 || len(loaded.Cands) != 12 {
		t.Fatalf("a mix loads by its slug: %v", err)
	}
}

func TestMixLeavesOutAPartInsideAnother(t *testing.T) {
	c := mixCatalog(t)
	ctx := context.Background()

	m, err := c.Mix(ctx, []string{"place-yunusobod-tumani", "place-toshkent"})
	if err != nil || m.Slug != "place-toshkent" {
		t.Fatalf("Yunusobod is in Toshkent: Toshkent's own map, got %v %v", m, err)
	}
	m, err = c.Mix(ctx, []string{"place-buxoro-viloyati", "uzbekistan", "place-yunusobod-tumani"})
	if err != nil || m.Slug != "uzbekistan" {
		t.Fatalf("everything is in Uzbekistan: got %v %v", m, err)
	}
	m, err = c.Mix(ctx, []string{"place-buxoro-viloyati", "place-buxoro-viloyati"})
	if err != nil || m.Slug != "place-buxoro-viloyati" {
		t.Fatalf("one part twice is that part: %v %v", m, err)
	}
	m, err = c.Mix(ctx, []string{"place-toshkent", "place-yunusobod-tumani", "place-buxoro-viloyati"})
	if err != nil || !m.Mix() || len(m.Parts()) != 2 || m.Parts()[0].Slug != "place-toshkent" {
		t.Fatalf("Toshkent and Buxoro: %v %v", m, err)
	}
	if other, _ := c.Mix(ctx, []string{"place-buxoro-viloyati", "place-toshkent"}); other == nil || other.ID != m.ID {
		t.Fatal("the same parts after leaving one out: the same mix")
	}
	m, err = c.Mix(ctx, []string{"tashkent-streets", "place-toshkent"})
	if err != nil || len(m.Parts()) != 2 {
		t.Fatalf("distinct maps of the same place: %v %v", m, err)
	}
	other, err := c.Mix(ctx, []string{"place-toshkent", "tashkent-streets"})
	if err != nil || other.Slug != m.Slug {
		t.Fatalf("the same maps in reverse order: %v %v", other, err)
	}
}

func TestMixRefusesWhatCantBeMixed(t *testing.T) {
	c := mixCatalog(t)
	ctx := context.Background()
	if _, err := c.Mix(ctx, nil); !errors.Is(err, ErrBadMix) {
		t.Fatalf("no parts: %v", err)
	}
	many := make([]string, MaxParts+1)
	for i := range many {
		many[i] = "place-buxoro-viloyati"
	}
	if _, err := c.Mix(ctx, many); !errors.Is(err, ErrBadMix) {
		t.Fatalf("13 parts: %v", err)
	}
	if _, err := c.Mix(ctx, []string{"place-buxoro-viloyati", "place-atlantida"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown place: %v", err)
	}
	if _, err := c.Mix(ctx, []string{"place-buxoro-viloyati", ""}); !errors.Is(err, ErrBadMix) {
		t.Fatalf("an empty slug: %v", err)
	}
	m, err := c.Mix(ctx, []string{"place-buxoro-viloyati", "place-samarqand-viloyati"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Mix(ctx, []string{m.Slug, "place-yunusobod-tumani"}); !errors.Is(err, ErrBadMix) {
		t.Fatalf("a mix in a mix: %v", err)
	}
}

func TestMixMadeAtOnceByManyIsOneMap(t *testing.T) {
	c := mixCatalog(t)
	ctx := context.Background()
	parts := []string{"place-yunusobod-tumani", "place-buxoro-viloyati"}
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// each its own catalog: nothing shared in memory, only the database
			m, err := NewCatalog(c.pool, c.reg, c.tree).Mix(ctx, parts)
			if err == nil && len(m.Cands) != 8 {
				err = fmt.Errorf("a part loaded with missing panoramas: got %d, want 8", len(m.Cands))
			}
			errs[i] = err
			if err == nil {
				ids[i] = m.ID
			}
		}()
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("request %d: %v (map %d, the first got %d)", i, errs[i], ids[i], ids[0])
		}
	}
}
