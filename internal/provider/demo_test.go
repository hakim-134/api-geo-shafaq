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
	r, err := NewRegistry([]string{"demo", "yandex"}, Yandex{}, Demo{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Allowed(nil); !got["demo"] || !got["yandex"] {
		t.Errorf("empty rule should allow every enabled provider: %v", got)
	}
	if got := r.Allowed([]string{"yandex", "google"}); len(got) != 1 || !got["yandex"] {
		t.Errorf("allowed: %v", got)
	}
	if _, err := NewRegistry([]string{"google"}, Yandex{}); err == nil {
		t.Error("unknown provider enabled")
	}
}
