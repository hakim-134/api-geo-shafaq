package config

import (
	"maps"
	"testing"
)

func TestCaps(t *testing.T) {
	got, err := caps(" google=160, yandex = 900 ,demo=0")
	if want := map[string]int{"google": 160, "yandex": 900, "demo": 0}; err != nil || !maps.Equal(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
	for _, bad := range []string{"google", "google=", "google=-1", "google=lots"} {
		if _, err := caps(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
