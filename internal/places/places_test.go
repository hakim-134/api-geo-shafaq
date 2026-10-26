package places

import (
	"strings"
	"testing"
)

func TestCleanUz(t *testing.T) {
	cases := map[string]string{
		"Farg'ona Viloyati":       "Farg‘ona viloyati",
		"Qoʻngʻirot Tumani":       "Qo‘ng‘irot tumani",
		"Nukus shahri hokimiyati": "Nukus shahri",
		"  Toshkent ":             "Toshkent",
	}
	for in, want := range cases {
		if got := CleanUz(in); got != want {
			t.Errorf("CleanUz(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanEn(t *testing.T) {
	cases := []struct{ kind, uz, en, want string }{
		{"district", "Yunusobod tumani", "Yunusabad district", "Yunusabad District"},
		{"district", "Uchtepa tumani", "Uchtepa tumani", "Uchtepa District"},
		{"district", "Bo‘ston tumani", "Bo'ston Distric", "Bo'ston District"},
		{"district", "Xiva tumani", "Khiva", "Khiva District"},
		{"district", "Bekobod", "Bekabad", "Bekabad"},
		{"city", "Andijon shahri", "Andijan City", "Andijan"},
		{"city", "Nukus shahri", "Nukus shahri", "Nukus"},
		{"region", "Andijon viloyati", "Andijan Region", "Andijan Region"},
	}
	for _, c := range cases {
		if got := CleanEn(c.kind, c.uz, c.en); got != c.want {
			t.Errorf("CleanEn(%q, %q) = %q, want %q", c.kind, c.en, got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Mirzo Ulug‘bek tumani":         "mirzo-ulugbek-tumani",
		"Qoraqalpog‘iston Respublikasi": "qoraqalpogiston-respublikasi",
		"Ташкент":                       "tashkent",
		"Yangi  Toshkent (yangi)":       "yangi-toshkent-yangi",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutlineSVG(t *testing.T) {
	svg, err := OutlineSVG(`{"type":"Polygon","coordinates":[[[69,41],[70,41],[70,42],[69,42],[69,41]]]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">`) || !strings.Contains(svg, "Z") {
		t.Fatalf("outline: %s", svg)
	}
	// a 1°×1° square at 41.5° N is wider than tall on a map by cos(41.5°): ~0.75
	if !strings.Contains(svg, "M14 98") && !strings.Contains(svg, "M14.1 98") {
		t.Errorf("the square is not squeezed by latitude: %s", svg)
	}
}
