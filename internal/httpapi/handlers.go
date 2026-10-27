package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/provider"
)

// ---------- system ----------

func (s *Server) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, oapi.Health{Ok: true, Version: &s.version})
}

func (s *Server) GetReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"db": "ok", "valkey": "ok"}
	ok := true
	if err := s.db.Ping(ctx); err != nil {
		checks["db"], ok = err.Error(), false
	}
	if s.valkey == nil {
		checks["valkey"], ok = "not configured", false
	} else if err := s.valkey.Ping(ctx).Err(); err != nil {
		checks["valkey"], ok = err.Error(), false
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, oapi.Health{Ok: ok, Version: &s.version, Checks: &checks})
}

// ---------- session ----------

func (s *Server) StartSession(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		if !s.allow(w, r, "guest:"+s.clientIP(r), 30, time.Hour) {
			return
		}
		nu, token, err := s.players.NewGuest(r.Context(), r.UserAgent())
		if err != nil {
			fail(w, r, err)
			return
		}
		s.setSession(w, token)
		u = &nu
	}
	s.writeMe(w, r, u)
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	if u := s.player(w, r); u != nil {
		s.writeMe(w, r, u)
	}
}

func (s *Server) writeMe(w http.ResponseWriter, r *http.Request, u *storeUser) {
	me, err := s.meOut(r.Context(), *u)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) ListMyGames(w http.ResponseWriter, r *http.Request, params oapi.ListMyGamesParams) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit >= 1 && *params.Limit <= 50 {
		limit = *params.Limit
	}
	var before *time.Time
	if params.Before != nil {
		t, err := decodeCursor(*params.Before)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_input")
			return
		}
		before = &t
	}
	offset := 0
	if params.Page != nil && *params.Page > 1 {
		offset = (*params.Page - 1) * limit
	}
	items, err := s.games.History(r.Context(), u.ID, before, offset, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	total, err := s.q.CountUserGames(r.Context(), u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	n := int(total)
	page := oapi.GamePage{Games: make([]oapi.GameListItem, 0, len(items)), Total: &n}
	for _, it := range items {
		page.Games = append(page.Games, oapi.GameListItem{
			Id: it.ID, Map: oapi.MapRef{Slug: it.MapSlug, Name: text(it.MapName)}, Status: oapi.GameStatus(it.Status),
			StartedAt: it.StartedAt, FinishedAt: it.FinishedAt, Total: it.Total, Max: it.Rounds * 5000, Rounds: it.Rounds,
		})
		if it.Players > 0 {
			page.Games[len(page.Games)-1].Players = &it.Players
		}
	}
	if len(items) == limit {
		next := encodeCursor(items[len(items)-1].StartedAt)
		page.Next = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func encodeCursor(t time.Time) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10)))
}

func decodeCursor(s string) (time.Time, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, err
	}
	us, err := strconv.ParseInt(string(b), 10, 64)
	return time.UnixMicro(us), err
}

// ---------- maps ----------

func (s *Server) ListMaps(w http.ResponseWriter, r *http.Request) {
	list, err := s.catalog.Official(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	pv, err := s.placeView(r, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]oapi.MapSummary, 0, len(list))
	for _, m := range list {
		out = append(out, mapSummary(m, s.games.Reg.Allowed(nil), s.games.Reg, pv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetMap(w http.ResponseWriter, r *http.Request, slug string) {
	m, err := s.catalog.Get(r.Context(), slug)
	if err != nil {
		fail(w, r, err)
		return
	}
	pv, err := s.placeView(r, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapSummary(m, s.games.Reg.Allowed(nil), s.games.Reg, pv))
}

func (s *Server) GetPlace(w http.ResponseWriter, r *http.Request, slug string) {
	pv, err := s.placeView(r, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	p := pv.tree.Get(slug)
	if p == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	out := oapi.PlaceDetail{Place: placeCard(p, pv), Path: []oapi.PlaceRef{}, Children: []oapi.PlaceCard{}}
	for _, a := range p.Path() {
		out.Path = append(out.Path, *placeRef(a))
	}
	for _, c := range p.Children {
		out.Children = append(out.Children, placeCard(c, pv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetPlaceShape(w http.ResponseWriter, r *http.Request, slug string) {
	p, err := s.q.PlaceShape(r.Context(), slug)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	var geometry map[string]any
	if err := json.Unmarshal([]byte(p.Geojson), &geometry); err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	b, _ := json.Marshal(oapi.PlaceShape{Slug: p.Slug, Geometry: geometry, Outline: p.OutlineSvg})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// placeView: the place tree, and with progress the player's rounds (none without a session).
func (s *Server) placeView(r *http.Request, progress bool) (placeView, error) {
	t, err := s.places.Get(r.Context())
	if err != nil {
		return placeView{}, err
	}
	pv := placeView{tree: t, progress: progress}
	if u := userFrom(r.Context()); u != nil && progress {
		if pv.rounds, err = places.UserRounds(r.Context(), s.q, u.ID); err != nil {
			return placeView{}, err
		}
	}
	return pv, nil
}

func (s *Server) gameOut(r *http.Request, g *game.Game, player oapi.PublicUser) (oapi.Game, error) {
	t, err := s.places.Get(r.Context())
	if err != nil {
		return oapi.Game{}, err
	}
	out := gameOut(g, s.games.Reg, t, player)
	if out.Room, err = s.roomTable(r.Context(), g); err != nil {
		return oapi.Game{}, err
	}
	return out, nil
}

// ---------- games ----------

func (s *Server) CreateGame(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.CreateGame
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "game:"+u.ID.String(), 120, time.Hour) {
		return
	}
	g, err := s.games.Create(r.Context(), u.ID, body.Map, rulesIn(body.Rules))
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.gameOut(r, g, publicUser(*u))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) FinishGame(w http.ResponseWriter, r *http.Request, id oapi.GameId) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	g, err := s.games.Finish(r.Context(), u.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.gameOut(r, g, publicUser(*u))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetGame(w http.ResponseWriter, r *http.Request, id oapi.GameId) {
	var uid = zeroUser
	if u := userFrom(r.Context()); u != nil {
		uid = u.ID
	}
	g, err := s.games.Get(r.Context(), uid, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	player, err := s.gamePlayer(r.Context(), g.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.gameOut(r, g, player)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) NextRound(w http.ResponseWriter, r *http.Request, id oapi.GameId) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	rd, err := s.games.NextRound(r.Context(), u.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, roundOut(rd))
}

func (s *Server) RoundShown(w http.ResponseWriter, r *http.Request, id oapi.GameId, n oapi.RoundN) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.RoundShown
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	rd, err := s.games.Shown(r.Context(), u.ID, id, n, pointIn(body.Pos))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, roundOut(rd))
}

func (s *Server) RoundFailed(w http.ResponseWriter, r *http.Request, id oapi.GameId, n oapi.RoundN) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.RoundFailed
	if !decode(r, &body) || (!body.Reason.Valid()) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	rd, err := s.games.Failed(r.Context(), u.ID, id, n, body.Reason == oapi.RoundFailedReasonNotFound)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, roundOut(rd))
}

func (s *Server) UseHint(w http.ResponseWriter, r *http.Request, id oapi.GameId, n oapi.RoundN) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	h, err := s.games.Hint(r.Context(), u.ID, id, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hintOut(*h))
}

func (s *Server) Guess(w http.ResponseWriter, r *http.Request, id oapi.GameId, n oapi.RoundN) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.GuessRequest
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	res, err := s.games.Guess(r.Context(), u.ID, id, n, pointIn(body.Pos))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resultOut(res))
}

// GetDemoPanorama draws the demo provider's test panorama (a JPEG).
func (s *Server) GetDemoPanorama(w http.ResponseWriter, r *http.Request, ref string) {
	id, err := s.demo.Open(ref)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	img, err := provider.DemoImage(id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	_, _ = w.Write(img)
}

func pointIn(p *oapi.LatLon) *geo.Point {
	if p == nil {
		return nil
	}
	return &geo.Point{Lat: p.Lat, Lon: p.Lon}
}
