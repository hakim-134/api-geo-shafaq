package httpapi

import (
	"context"
	"net/http"

	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// mayStart answers whether this player may start a game or room on this map: another player's
// private map doesn't exist for them. The player's own map of missed streets is refreshed here, so
// each new game plays what is missed right now.
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
