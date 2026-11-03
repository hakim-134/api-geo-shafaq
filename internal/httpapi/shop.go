package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/barg"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

func earnedOut(in []barg.Earned) *[]oapi.Earned {
	if len(in) == 0 {
		return nil
	}
	out := make([]oapi.Earned, len(in))
	for i, e := range in {
		out[i] = oapi.Earned{Reason: oapi.EarnedReason(e.Reason), Amount: e.Amount}
		if e.Note != "" {
			note := e.Note
			out[i].Note = &note
		}
	}
	return &out
}

func (s *Server) GetWallet(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	balance, err := s.q.BargBalance(r.Context(), u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	rows, err := s.q.BargHistory(r.Context(), u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := oapi.Wallet{Balance: int(balance), Entries: make([]oapi.WalletEntry, len(rows))}
	for i, row := range rows {
		out.Entries[i] = oapi.WalletEntry{Amount: int(row.Amount), At: row.CreatedAt, Reason: oapi.WalletEntryReason(row.Reason), Ref: row.Ref}
		if row.Note != "" {
			note := row.Note
			out.Entries[i].Note = &note
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// shopOut is the shop as this player sees it.
func (s *Server) shopOut(ctx context.Context, u store.User) (oapi.Shop, error) {
	balance, err := s.q.BargBalance(ctx, u.ID)
	if err != nil {
		return oapi.Shop{}, err
	}
	owned, err := s.q.OwnedItems(ctx, u.ID)
	if err != nil {
		return oapi.Shop{}, err
	}
	prefs := players.UserPrefs(u)
	worn := []string{prefs.Frame, prefs.Pin, prefs.Trail}
	out := oapi.Shop{Balance: int(balance), Items: make([]oapi.ShopItem, len(barg.Shop))}
	for i, it := range barg.Shop {
		out.Items[i] = oapi.ShopItem{
			Id: it.ID, Kind: oapi.ShopItemKind(it.Kind), Price: it.Price,
			Owned: slices.Contains(owned, it.ID), Worn: slices.Contains(worn, it.ID),
		}
	}
	return out, nil
}

func (s *Server) writeShop(w http.ResponseWriter, r *http.Request, u store.User) {
	out, err := s.shopOut(r.Context(), u)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetShop(w http.ResponseWriter, r *http.Request) {
	if u := s.player(w, r); u != nil {
		s.writeShop(w, r, *u)
	}
}

func (s *Server) BuyItem(w http.ResponseWriter, r *http.Request, item string) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "shop:"+u.ID.String(), 120, time.Hour) {
		return
	}
	if err := barg.Buy(r.Context(), s.db, u.ID, item); err != nil {
		fail(w, r, err)
		return
	}
	s.writeShop(w, r, *u)
}

// wearKey: where a kind's worn item is kept in the player's preferences.
func wearKey(kind string) string {
	return map[string]string{barg.KindFrame: "frame", barg.KindPin: "pin", barg.KindTrail: "trail"}[kind]
}

func (s *Server) WearItem(w http.ResponseWriter, r *http.Request, item string) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.WearItemJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	it, ok := barg.Find(item)
	if !ok {
		fail(w, r, barg.ErrNoItem)
		return
	}
	key := wearKey(it.Kind)
	if key == "" {
		fail(w, r, barg.ErrNotWearable)
		return
	}
	if !s.allow(w, r, "shop:"+u.ID.String(), 120, time.Hour) {
		return
	}
	owned, err := s.q.OwnedItems(r.Context(), u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !slices.Contains(owned, it.ID) {
		fail(w, r, barg.ErrNotOwned)
		return
	}
	var nu store.User
	if body.On {
		nu, err = s.q.WearItem(r.Context(), store.WearItemParams{ID: u.ID, Prefs: []byte(`{"` + key + `":"` + it.ID + `"}`)})
	} else {
		nu, err = s.q.TakeOffItem(r.Context(), store.TakeOffItemParams{ID: u.ID, Key: key})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = players.ErrNotFound
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeShop(w, r, nu)
}
