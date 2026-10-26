package httpapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Conversions between the services' types and the contract's.

type storeUser = store.User

var zeroUser uuid.UUID

func text(t places.Text) oapi.Text { return oapi.Text{Uz: t.Uz, Ru: t.Ru, En: t.En} }

func textPtr(t *places.Text) *oapi.Text {
	if t == nil {
		return nil
	}
	out := text(*t)
	return &out
}

func point(p geo.Point) oapi.LatLon { return oapi.LatLon{Lat: p.Lat, Lon: p.Lon} }

// mapSummary counts only the players' providers (allowed), never test ones.
func mapSummary(m *maps.Map, allowed map[string]bool, seen map[int64]bool) oapi.MapSummary {
	shown := m.Shown(allowed)
	out := oapi.MapSummary{
		Slug: m.Slug, Name: text(m.Name), Description: text(m.Desc), Official: m.Official,
		Panoramas: shown.Panoramas, Groups: shown.Groups, Bbox: m.BBox[:], SizeKm: m.SizeKm,
		Rules: rulesOut(m.Rules), Providers: shown.Providers,
	}
	if m.Outline != "" {
		out.Outline = &m.Outline
	}
	if m.Pick.StreetsOnly {
		streets := true
		out.Streets = &streets
		out.Groups = shown.Streets
		if seen != nil {
			n := 0
			counted := map[int64]bool{}
			for _, c := range m.Cands {
				if allowed[c.Provider] && seen[c.StreetID] && !counted[c.StreetID] {
					counted[c.StreetID] = true
					n++
				}
			}
			out.Seen = &n
		}
	}
	return out
}

func rulesOut(r rules.Rules) oapi.Rules {
	providers := r.Providers
	if providers == nil {
		providers = []string{}
	}
	return oapi.Rules{
		Rounds: r.Rounds, TimeSec: r.TimeSec, Movement: oapi.Movement(r.Movement),
		Hints:   oapi.HintRule{Count: r.Hints.Count, Per: oapi.HintRulePer(r.Hints.Per), Cost: r.Hints.Cost},
		Compass: r.Compass, Providers: providers, ScoreScale: r.ScoreScale,
		PanoLabels: r.PanoLabels, MapLabels: r.MapLabels,
	}
}

func rulesIn(r *oapi.Rules) *rules.Rules {
	if r == nil {
		return nil
	}
	return &rules.Rules{
		Rounds: r.Rounds, TimeSec: r.TimeSec, Movement: rules.Movement(r.Movement),
		Hints:   rules.Hints{Count: r.Hints.Count, Per: rules.HintPer(r.Hints.Per), Cost: r.Hints.Cost},
		Compass: r.Compass, Providers: r.Providers, ScoreScale: r.ScoreScale,
		PanoLabels: r.PanoLabels, MapLabels: r.MapLabels,
	}
}

// gameOut describes the map as this game plays it: counted for the game's providers.
func gameOut(g *game.Game, allowed map[string]bool, player oapi.PublicUser) oapi.Game {
	out := oapi.Game{
		Id: g.ID, Player: player, Map: mapSummary(g.Map, allowed, nil), Rules: rulesOut(g.Rules), Status: oapi.GameStatus(g.Status),
		StartedAt: g.StartedAt, FinishedAt: g.FinishedAt, Total: g.Total, Max: g.Max(),
		Rounds: make([]oapi.Round, 0, len(g.Rounds)),
	}
	if g.Rules.Hints.Per == rules.PerGame {
		left := g.HintsLeft()
		out.HintsLeft = &left
	}
	for i := range g.Rounds {
		out.Rounds = append(out.Rounds, roundOut(&g.Rounds[i]))
	}
	return out
}

func roundOut(r *game.Round) oapi.Round {
	out := oapi.Round{
		N: r.N, View: viewOut(r.View), ShownAt: r.ShownAt, Deadline: r.Deadline,
		HintsUsed: len(r.Hints), HintsLeft: r.HintsLeft,
	}
	if r.Remaining != nil {
		ms := int(*r.Remaining / time.Millisecond)
		out.RemainingMs = &ms
	}
	if len(r.Hints) > 0 {
		hints := make([]oapi.Hint, len(r.Hints))
		for i, h := range r.Hints {
			hints[i] = hintOut(h)
		}
		out.Hints = &hints
	}
	if r.Result != nil {
		res := resultOut(r.Result)
		out.Result = &res
	}
	return out
}

func viewOut(v provider.View) oapi.PanoView {
	out := oapi.PanoView{Provider: v.Provider}
	if v.Near != nil {
		near := point(*v.Near)
		out.Near = &near
		out.SnapKm = &v.SnapKm
	}
	if v.Ref != "" {
		out.Ref = &v.Ref
	}
	if v.ImageURL != "" {
		out.ImageUrl = &v.ImageURL
	}
	if v.GuessMap != "" {
		out.GuessMap = &v.GuessMap
	}
	heading := v.Heading
	out.Heading = &heading
	return out
}

func hintOut(h game.Hint) oapi.Hint {
	return oapi.Hint{
		Kind: oapi.HintKind(h.Kind), Text: text(h.Text),
		Circle: oapi.Circle{Center: point(h.Center), RadiusKm: h.RadiusKm},
	}
}

func resultOut(r *game.Result) oapi.RoundResult {
	out := oapi.RoundResult{
		Actual: point(r.Actual), Km: r.Km, Score: r.Score, HintsUsed: r.HintsUsed, Late: r.Late,
		Place: oapi.PlaceInfo{
			Street: textPtr(r.Place.Street), District: textPtr(r.Place.District),
			City: textPtr(r.Place.City), Region: textPtr(r.Place.Region),
		},
	}
	if r.Guess != nil {
		g := point(*r.Guess)
		out.Guess = &g
	}
	if r.Place.Metro != nil {
		out.Place.Metro = &oapi.Metro{Name: text(r.Place.Metro.Name), Km: r.Place.Metro.Km}
	}
	gameOver, total := r.GameOver, r.Total
	out.GameOver, out.Total = &gameOver, &total
	return out
}
