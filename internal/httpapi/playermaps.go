package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/geo"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// searchPage is how many maps a page of the search holds.
const searchPage = 20

// mayStart answers whether this player may start a game or room on this map: others' private maps
// and deleted maps don't exist for them. The player's own map of missed streets is refreshed here,
// so each new game plays what is missed right now.
func (s *Server) mayStart(ctx context.Context, u *store.User, slug string) error {
	if slug == maps.MissMap(u.ID) {
		if err := s.catalog.SyncMissed(ctx, u.ID); err != nil {
			return err
		}
	}
	m, err := s.catalog.Get(ctx, slug)
	if err != nil {
		return err
	}
	if !m.Open(u.ID) {
		return maps.ErrNotFound
	}
	return nil
}

func sourcesIn(places *[]string, streets *[]int64, area *[][]float64) (maps.Sources, bool) {
	var out maps.Sources
	if places != nil {
		out.Places = *places
	}
	if streets != nil {
		out.Streets = *streets
	}
	if area != nil {
		for _, p := range *area {
			if len(p) != 2 {
				return out, false
			}
			out.Area = append(out.Area, geo.Point{Lat: p[0], Lon: p[1]})
		}
	}
	return out, out.Valid()
}

func (s *Server) PreviewMap(w http.ResponseWriter, r *http.Request) {
	var body oapi.MapSources
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	src, ok := sourcesIn(body.Places, body.Streets, body.Area)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "map-preview:"+s.clientIP(r), 600, time.Hour) {
		return
	}
	n, err := s.catalog.Preview(r.Context(), src)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, oapi.MapPreview{Panoramas: int(n.Panoramas), Streets: int(n.Streets)})
}

func (s *Server) CreateMap(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.NewMap
	if !decode(r, &body) || !body.Visibility.Valid() {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	name, err := players.CleanMapName(body.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	src, ok := sourcesIn(body.Places, body.Streets, body.Area)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "map-new:"+u.ID.String(), 30, 24*time.Hour) {
		return
	}
	var tags []string
	if body.Tags != nil {
		tags = *body.Tags
	}
	m, err := s.catalog.CreatePlayerMap(r.Context(), u.ID, name, string(body.Visibility), tags, src)
	if err != nil {
		fail(w, r, err)
		return
	}
	pv, err := s.placeView(r, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, mapSummary(m, s.games.Reg.Allowed(nil), s.games.Reg, pv))
}

// card is one row of a list of maps.
func (s *Server) card(ctx context.Context, row store.Map, plays, likes int32) (oapi.MapCard, error) {
	m, err := s.catalog.Get(ctx, row.Slug)
	if err != nil {
		return oapi.MapCard{}, err
	}
	return oapi.MapCard{
		Slug: row.Slug, Name: text(m.Name), Description: text(m.Desc), Official: m.Official,
		Panoramas: m.Shown(s.games.Reg.Allowed(nil)).Panoramas, Plays: int(plays), Likes: int(likes),
		Tags: row.Tags, Visibility: oapi.MapVisibility(row.Visibility), Moderation: oapi.MapModeration(row.Moderation),
	}, nil
}

func (s *Server) ListMyMaps(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	rows, err := s.q.MyMaps(r.Context(), &u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]oapi.MapCard, 0, len(rows))
	for _, row := range rows {
		c, err := s.card(r.Context(), store.Map{Slug: row.Slug, Tags: row.Tags, Visibility: row.Visibility, Moderation: row.Moderation}, row.Plays, row.Likes)
		if err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// apostrophes is every way a phone writes o‘: names are stored the game's way.
var apostrophes = strings.NewReplacer("'", "‘", "’", "‘", "ʻ", "‘", "ʼ", "‘", "`", "‘")

func (s *Server) SearchMaps(w http.ResponseWriter, r *http.Request, params oapi.SearchMapsParams) {
	var q, tag string
	if params.Q != nil {
		q = apostrophes.Replace(strings.TrimSpace(*params.Q))
	}
	if params.Tag != nil {
		tag = *params.Tag
	}
	page := 1
	if params.Page != nil && *params.Page > 1 {
		page = *params.Page
	}
	if !s.allow(w, r, "map-search:"+s.clientIP(r), 600, time.Hour) {
		return
	}
	rows, err := s.q.SearchMaps(r.Context(), store.SearchMapsParams{Q: q, Tag: tag, Lim: searchPage, Off: int32((page - 1) * searchPage)})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := oapi.MapSearch{Maps: make([]oapi.MapCard, 0, len(rows))}
	for _, row := range rows {
		c, err := s.card(r.Context(), store.Map{Slug: row.Slug, Tags: row.Tags, Visibility: row.Visibility, Moderation: row.Moderation}, row.Plays, row.Likes)
		if err != nil {
			fail(w, r, err)
			return
		}
		out.Maps = append(out.Maps, c)
		out.Total = int(row.Total)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetMapPage(w http.ResponseWriter, r *http.Request, slug oapi.Slug) {
	out, err := s.mapPage(r, slug)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mapPage(r *http.Request, slug string) (oapi.MapPage, error) {
	ctx := r.Context()
	uid := zeroUser
	if u := userFrom(ctx); u != nil {
		uid = u.ID
	}
	m, err := s.catalog.Get(ctx, slug)
	if err != nil {
		return oapi.MapPage{}, err
	}
	if !m.Open(uid) {
		return oapi.MapPage{}, maps.ErrNotFound
	}
	row, err := s.q.GetMapBySlug(ctx, slug) // moderation changes outside this process
	if err != nil {
		return oapi.MapPage{}, err
	}
	stats, err := s.q.MapStats(ctx, store.MapStatsParams{MapID: m.ID, UserID: uid})
	if err != nil {
		return oapi.MapPage{}, err
	}
	allowed := s.games.Reg.Allowed(nil)
	board, err := s.q.MapBoard(ctx, store.MapBoardParams{MapID: m.ID, MinRounds: int32(min(3, max(m.Count(allowed), 1)))})
	if err != nil {
		return oapi.MapPage{}, err
	}
	pv, err := s.placeView(r, true)
	if err != nil {
		return oapi.MapPage{}, err
	}
	out := oapi.MapPage{
		Map: mapSummary(m, allowed, s.games.Reg, pv), Visibility: oapi.MapVisibility(row.Visibility),
		Moderation: oapi.MapModeration(row.Moderation), Tags: row.Tags,
		Plays: int(stats.Plays), Likes: int(stats.Likes), Liked: stats.Liked,
		Mine: m.Owner != nil && *m.Owner == uid, Board: make([]oapi.MapBoardRow, len(board)),
	}
	shape, err := s.q.MapAreaShape(ctx, m.ID)
	if err != nil {
		return oapi.MapPage{}, err
	}
	if shape != "" {
		var area map[string]any
		if err := json.Unmarshal([]byte(shape), &area); err != nil {
			return oapi.MapPage{}, err
		}
		out.Area = &area
	}
	points, err := s.q.MapStreetPoints(ctx, store.MapStreetPointsParams{MapID: m.ID, Providers: s.games.Reg.Names()})
	if err != nil {
		return oapi.MapPage{}, err
	}
	if len(points) > 0 {
		pts := make([][]float64, len(points))
		for i, p := range points {
			pts[i] = []float64{p.Lat, p.Lon}
		}
		out.StreetPoints = &pts
	}
	if out.Mine && row.ModerationNote != "" {
		out.ModerationNote = &row.ModerationNote
	}
	if m.Owner != nil {
		by, err := s.gamePlayer(ctx, *m.Owner)
		if err != nil {
			return oapi.MapPage{}, err
		}
		out.By = &by
	}
	for i, b := range board {
		out.Board[i] = oapi.MapBoardRow{Place: i + 1, Player: publicUser(b.User), PerRound: int(b.PerRound + 0.5), Game: b.GameID}
	}
	return out, nil
}

// ownMap is the player's own map, for changing it; anyone else gets not found.
func (s *Server) ownMap(w http.ResponseWriter, r *http.Request, slug string) (store.Map, bool) {
	u := s.player(w, r)
	if u == nil {
		return store.Map{}, false
	}
	row, err := s.q.GetMapBySlug(r.Context(), slug)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (row.OwnerID == nil || *row.OwnerID != u.ID || row.DeletedAt != nil || maps.IsMiss(slug))) {
		writeError(w, http.StatusNotFound, "not_found")
		return store.Map{}, false
	}
	if err != nil {
		fail(w, r, err)
		return store.Map{}, false
	}
	return row, true
}

func (s *Server) UpdateMap(w http.ResponseWriter, r *http.Request, slug oapi.Slug) {
	var body oapi.MapUpdate
	if !decode(r, &body) || !body.Visibility.Valid() || !maps.ValidTags(body.Tags) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	row, ok := s.ownMap(w, r, slug)
	if !ok {
		return
	}
	name, err := players.CleanMapName(body.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	// what a person approved stays approved only as it was
	moderation := maps.ModerationFor(string(body.Visibility))
	if row.Moderation == "approved" && moderation == "pending" && name == row.NameUz && slices.Equal(body.Tags, row.Tags) {
		moderation = "approved"
	}
	if err := s.q.UpdatePlayerMap(r.Context(), store.UpdatePlayerMapParams{
		ID: row.ID, NameUz: name, Visibility: string(body.Visibility), Moderation: moderation, Tags: body.Tags,
	}); err != nil {
		fail(w, r, err)
		return
	}
	s.catalog.Forget(slug)
	out, err := s.mapPage(r, slug)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) DeleteMap(w http.ResponseWriter, r *http.Request, slug oapi.Slug) {
	row, ok := s.ownMap(w, r, slug)
	if !ok {
		return
	}
	if err := s.q.DeletePlayerMap(r.Context(), row.ID); err != nil {
		fail(w, r, err)
		return
	}
	s.catalog.Forget(slug)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) LikeMap(w http.ResponseWriter, r *http.Request, slug oapi.Slug) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if !s.allow(w, r, "map-like:"+u.ID.String(), 200, time.Hour) {
		return
	}
	m, err := s.catalog.Get(r.Context(), slug)
	if err == nil && !m.Open(u.ID) {
		err = maps.ErrNotFound
	}
	if err == nil {
		err = s.q.LikeMap(r.Context(), store.LikeMapParams{MapID: m.ID, UserID: u.ID})
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) UnlikeMap(w http.ResponseWriter, r *http.Request, slug oapi.Slug) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	m, err := s.catalog.Get(r.Context(), slug)
	if err == nil {
		err = s.q.UnlikeMap(r.Context(), store.UnlikeMapParams{MapID: m.ID, UserID: u.ID})
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) LibraryPoints(w http.ResponseWriter, r *http.Request, params oapi.LibraryPointsParams) {
	width, height := params.East-params.West, params.North-params.South
	if width <= 0 || height <= 0 || width > 30 || height > 30 {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "map-points:"+s.clientIP(r), 1200, time.Hour) {
		return
	}
	rows, err := s.q.LibraryPoints(r.Context(), store.LibraryPointsParams{
		Cell: max(width, height) / 32, Providers: s.games.Reg.Names(),
		West: params.West, South: params.South, East: params.East, North: params.North,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([][]float64, len(rows))
	for i, p := range rows {
		out[i] = []float64{p.Lat, p.Lon}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) LibraryStreets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.NamedStreets(r.Context(), s.games.Reg.Names())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]oapi.LibraryStreet, len(rows))
	for i, row := range rows {
		out[i] = oapi.LibraryStreet{Id: row.ID, Name: text(places.NewText(row.NameUz, row.NameRu, "")), Panoramas: int(row.Panoramas)}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetMissedStreets(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	rows, err := s.q.MissedStreets(r.Context(), store.MissedStreetsParams{UserID: u.ID, Providers: s.games.Reg.Names(), LearnedKm: places.LearnedKm})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := oapi.MissedStreets{Map: maps.MissMap(u.ID), Streets: make([]oapi.MyStreet, len(rows))}
	for i, row := range rows {
		place := text(places.NewText(row.PlaceUz, row.PlaceRu, row.PlaceEn))
		out.Streets[i] = oapi.MyStreet{Name: text(places.NewText(row.NameUz, row.NameRu, "")), Place: &place, Rounds: int(row.Rounds)}
		if row.BestKm >= 0 {
			km := row.BestKm
			out.Streets[i].BestKm = &km
		}
		at, heading := oapi.LatLon{Lat: row.AtLat, Lon: row.AtLon}, row.Heading
		out.Streets[i].At, out.Streets[i].Heading = &at, &heading
	}
	writeJSON(w, http.StatusOK, out)
}
