package httpapi

import "testing"

func TestOrigins(t *testing.T) {
	o := Origins{"https://geo.shafaq.uz", "http://localhost:*"}
	for origin, want := range map[string]bool{
		"https://geo.shafaq.uz":         true,
		"http://localhost:5173":         true,
		"http://localhost":              true,
		"https://geo.shafaq.uz.evil.io": false,
		"http://geo.shafaq.uz":          false,
		"https://localhost:5173":        false,
		"null":                          false,
		"":                              false,
	} {
		if got := o.Allowed(origin); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", origin, got, want)
		}
	}
}
