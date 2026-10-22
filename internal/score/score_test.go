package score

import "testing"

func TestRound(t *testing.T) {
	const tashkent = 40.0 // D, km
	cases := []struct {
		name         string
		km, size, sc float64
		want         int
	}{
		{"exact", 0, tashkent, 1, 5000},
		{"within D/1000", 0.04, tashkent, 1, 5000},
		{"D/10 away is 5000/e", 4, tashkent, 1, 1839},
		{"forgiving doubles D", 8, tashkent, 2, 1839},
		{"far", 60, tashkent, 1, 0},
		{"negative distance", -1, tashkent, 1, 0},
		{"no size", 1, 0, 1, 0},
	}
	for _, c := range cases {
		if got := Round(c.km, c.size, c.sc); got != c.want {
			t.Errorf("%s: Round(%v, %v, %v) = %d, want %d", c.name, c.km, c.size, c.sc, got, c.want)
		}
	}
}

func TestWithHints(t *testing.T) {
	if got := WithHints(1839, 1, 0.2); got != 1471 {
		t.Errorf("one hint at 20%%: %d, want 1471 (v0)", got)
	}
	if got := WithHints(5000, 2, 0.2); got != 3200 {
		t.Errorf("two hints: %d", got)
	}
	if got := WithHints(5000, 3, 0); got != 5000 {
		t.Errorf("free hints cost something: %d", got)
	}
}
