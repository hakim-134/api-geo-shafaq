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

// placeView is what map summaries and place cards need besides the map: the place tree,
// and the player's rounds when progress is wanted.
type placeView struct {
	tree     *places.Tree
	rounds   []places.Round
	progress bool
}

func placeRef(p *places.Place) *oapi.PlaceRef {
	if p == nil {
		return nil
	}
	return &oapi.PlaceRef{Slug: p.Slug, Kind: oapi.PlaceRefKind(p.Kind), Name: text(p.Name), Map: maps.PlaceMap(p.Slug)}
}

func progressOut(p places.Progress) oapi.Progress {
	return oapi.Progress{Unit: oapi.ProgressUnit(p.Unit), Total: p.Total, Seen: p.Seen, Learned: p.Learned}
}

func placeCard(p *places.Place, pv placeView) oapi.PlaceCard {
	out := oapi.PlaceCard{
		Slug: p.Slug, Kind: oapi.PlaceCardKind(p.Kind), Name: text(p.Name), Map: maps.PlaceMap(p.Slug),
		Outline: p.Outline, Panoramas: p.Panoramas, Places: len(p.Playable()),
		Progress: progressOut(p.Progress(pv.rounds)),
	}
	if p.Banner != "" {
		out.Banner, out.BannerCredit = &p.Banner, &p.BannerCredit
	}
	return out
}

// mapSummary counts only the players' providers (allowed), never test ones.
func mapSummary(m *maps.Map, allowed map[string]bool, reg *provider.Registry, pv placeView) oapi.MapSummary {
	shown := m.Shown(allowed)
	canMove := reg.CanMove(allowed)
	out := oapi.MapSummary{
		Slug: m.Slug, Name: text(m.Name), Description: text(m.Desc), Official: m.Official,
		Panoramas: shown.Panoramas, Groups: shown.Groups, Bbox: m.BBox[:], SizeKm: m.SizeKm,
		Rules: rulesOut(m.Rules), Providers: shown.Providers, CanMove: &canMove,
	}
	if m.Outline != "" {
		out.Outline = &m.Outline
	}
	if m.Pick.StreetsOnly {
		out.Groups = shown.Streets
	}
	if pv.tree != nil {
		slugs := make([]string, 0, len(m.Places))
		for _, id := range m.Places {
			if p := pv.tree.ByID(id); p != nil {
				slugs = append(slugs, p.Slug)
			}
		}
		out.Places = &slugs
	}
	if p := m.Place(pv.tree); p != nil {
		out.Place, out.Parent = placeRef(p), placeRef(p.Parent)
		if pv.progress {
			pr := progressOut(p.Progress(pv.rounds))
			out.Progress = &pr
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
		Hints:   hintRuleOut(r.Hints),
		Compass: r.Compass, Providers: providers, ScoreScale: r.ScoreScale,
		PanoLabels: r.PanoLabels, MapLabels: r.MapLabels,
	}
}

func hintRuleOut(h rules.Hints) oapi.HintRule {
	out := oapi.HintRule{Count: h.Count, Per: oapi.HintRulePer(h.Per), Cost: h.Cost}
	if h.Kind != rules.HintAuto {
		k := oapi.HintRuleKind(h.Kind)
		out.Kind = &k
	}
	return out
}

func rulesIn(r *oapi.Rules) *rules.Rules {
	if r == nil {
		return nil
	}
	hints := rules.Hints{Count: r.Hints.Count, Per: rules.HintPer(r.Hints.Per), Cost: r.Hints.Cost}
	if r.Hints.Kind != nil {
		hints.Kind = rules.HintKind(*r.Hints.Kind)
	}
	return &rules.Rules{
		Rounds: r.Rounds, TimeSec: r.TimeSec, Movement: rules.Movement(r.Movement),
		Hints:   hints,
		Compass: r.Compass, Providers: r.Providers, ScoreScale: r.ScoreScale,
		PanoLabels: r.PanoLabels, MapLabels: r.MapLabels,
	}
}

// gameOut describes the map as this game plays it: counted for the game's providers.
func gameOut(g *game.Game, reg *provider.Registry, tree *places.Tree, player oapi.PublicUser) oapi.Game {
	out := oapi.Game{
		Id: g.ID, Player: player, Map: mapSummary(g.Map, reg.Allowed(g.Rules.Providers), reg, placeView{tree: tree}), Rules: rulesOut(g.Rules), Status: oapi.GameStatus(g.Status),
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
	out := oapi.Hint{Kind: oapi.HintKind(h.Kind)}
	if h.Text != (places.Text{}) {
		out.Text = textPtr(&h.Text)
	}
	if h.Place != "" {
		out.Place = &h.Place
	}
	if h.RadiusKm > 0 {
		out.Circle = &oapi.Circle{Center: point(h.Center), RadiusKm: h.RadiusKm}
	}
	return out
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
	if r.Base > 0 {
		out.Base = &r.Base
	}
	if r.Place.Metro != nil {
		out.Place.Metro = &oapi.Metro{Name: text(r.Place.Metro.Name), Km: r.Place.Metro.Km}
	}
	gameOver, total := r.GameOver, r.Total
	out.GameOver, out.Total = &gameOver, &total
	return out
}
