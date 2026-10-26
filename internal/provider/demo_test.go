package provider

import (
	"strings"
	"testing"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
)

func TestDemoRef(t *testing.T) {
	d := Demo{Secret: []byte("s"), BaseURL: "http://api"}
	v := d.View(Pano{ID: 4242}, geo.Point{}, 0, 90)
	if v.Near != nil {
		t.Fatal("the demo provider must not send a position")
	}
	id, err := d.Open(v.Ref)
	if err != nil || id != 4242 {
		t.Fatalf("Open(%q) = %d, %v", v.Ref, id, err)
	}
	if !strings.HasSuffix(v.ImageURL, "/v1/pano/demo/"+v.Ref) {
		t.Fatalf("image url %q", v.ImageURL)
	}
	other := Demo{Secret: []byte("other")}
	if _, err := other.Open(v.Ref); err == nil {
		t.Fatal("a reference signed with another secret was accepted")
	}
	if _, err := d.Open("nonsense"); err == nil {
		t.Fatal("nonsense accepted")
	}
}

func TestRegistry(t *testing.T) {
	r, err := NewRegistry([]string{"yandex"}, []string{"demo"}, Yandex{}, Demo{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Allowed(nil); len(got) != 1 || !got["yandex"] {
		t.Errorf("empty rule should allow only the players' providers: %v", got)
	}
	if got := r.Allowed([]string{"demo"}); len(got) != 1 || !got["demo"] {
		t.Errorf("a test provider asked for by name: %v", got)
	}
	if got := r.Allowed([]string{"yandex", "google"}); len(got) != 1 || !got["yandex"] {
		t.Errorf("allowed: %v", got)
	}
	if _, ok := r.Get("demo"); !ok {
		t.Error("a test provider must be found, so its games can be read")
	}
	if got := r.Names(); len(got) != 1 || got[0] != "yandex" {
		t.Errorf("names: %v", got)
	}
	if got := r.Known(); len(got) != 2 {
		t.Errorf("known: %v", got)
	}
	if _, err := NewRegistry([]string{"google"}, nil, Yandex{}); err == nil {
		t.Error("unknown provider enabled")
	}
	if _, err := NewRegistry([]string{"demo"}, []string{"demo"}, Demo{}); err == nil {
		t.Error("a provider in both tiers")
	}
}
