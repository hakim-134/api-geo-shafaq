package rules

import "testing"

func TestValidate(t *testing.T) {
	known := []string{"yandex", "demo"}
	for name, r := range map[string]Rules{"easy": Easy, "normal": Normal, "hard": Hard} {
		if err := r.Validate(known); err != nil {
			t.Errorf("preset %s is invalid", name)
		}
	}
	bad := map[string]func(*Rules){
		"no rounds":        func(r *Rules) { r.Rounds = 0 },
		"too many rounds":  func(r *Rules) { r.Rounds = 21 },
		"5 s rounds":       func(r *Rules) { r.TimeSec = 5 },
		"11 min rounds":    func(r *Rules) { r.TimeSec = 660 },
		"unknown movement": func(r *Rules) { r.Movement = "fly" },
		"negative hints":   func(r *Rules) { r.Hints.Count = -1 },
		"hint per week":    func(r *Rules) { r.Hints.Per = "week" },
		"hint costs 150%":  func(r *Rules) { r.Hints.Cost = 1.5 },
		"scale 0":          func(r *Rules) { r.ScoreScale = 0 },
		"unknown provider": func(r *Rules) { r.Providers = []string{"google"} },
	}
	for name, change := range bad {
		r := Normal
		change(&r)
		if r.Validate(known) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHintsLeft(t *testing.T) {
	perRound := Rules{Hints: Hints{Count: 2, Per: PerRound}}
	if got := perRound.HintsLeft(1, 5); got != 1 {
		t.Errorf("per round: %d", got)
	}
	perGame := Rules{Hints: Hints{Count: 3, Per: PerGame}}
	if got := perGame.HintsLeft(1, 3); got != 0 {
		t.Errorf("per game: %d", got)
	}
}
