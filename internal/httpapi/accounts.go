package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/players"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// telegramBot knows the players' bot. Its username comes from Telegram on first use and is
// kept once known; until then each use asks again (at most once a minute).
type telegramBot struct {
	token  string
	client *http.Client

	mu    sync.Mutex
	name  string
	tried time.Time
}

func (b *telegramBot) Name(ctx context.Context) string {
	if b == nil || b.token == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.name != "" || time.Since(b.tried) < time.Minute {
		return b.name
	}
	b.tried = time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	name, err := auth.TelegramBotName(ctx, b.client, b.token)
	if err != nil {
		slog.WarnContext(ctx, "telegram bot name", "err", err)
		return ""
	}
	b.name = name
	return name
}

// ---------- sign-in ----------

func (s *Server) GetConfig(w http.ResponseWriter, r *http.Request) {
	out := oapi.AppConfig{Avatars: players.Avatars}
	if name := s.bot.Name(r.Context()); name != "" {
		out.TelegramBot = &name
	}
	if s.google.ClientID != "" {
		out.GoogleClientId = &s.google.ClientID
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) StartTelegramLogin(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	name := s.bot.Name(r.Context())
	if name == "" {
		writeError(w, http.StatusServiceUnavailable, "not_configured")
		return
	}
	if !s.allow(w, r, "tglogin:"+u.ID.String(), 30, time.Hour) {
		return
	}
	code, err := s.players.StartTelegram(r.Context(), *u)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, oapi.TelegramStart{
		Code: code, Url: "https://t.me/" + name + "?start=login_" + url.QueryEscape(code),
		ExpiresIn: int(players.TelegramCodeTTL.Seconds()),
	})
}

func (s *Server) CheckTelegramLogin(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.CheckTelegramLoginJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	// the game asks every two seconds for up to ten minutes
	if !s.allow(w, r, "tgcheck:"+u.ID.String(), 600, 10*time.Minute) {
		return
	}
	out, err := s.players.CheckTelegram(r.Context(), *u, r.UserAgent(), body.Code)
	s.writeAuth(w, r, out, err)
}

func (s *Server) TelegramWebAppLogin(w http.ResponseWriter, r *http.Request) {
	var body oapi.TelegramWebAppLoginJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "login:"+s.clientIP(r), 60, time.Hour) {
		return
	}
	tu, err := auth.TelegramWebApp(body.InitData, s.bot.token, time.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_login")
		return
	}
	out, err := s.players.SignIn(r.Context(), userFrom(r.Context()), r.UserAgent(), tu.Identity())
	s.writeAuth(w, r, out, err)
}

func (s *Server) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.google.ClientID == "" {
		writeError(w, http.StatusServiceUnavailable, "not_configured")
		return
	}
	var body oapi.GoogleLoginJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "login:"+s.clientIP(r), 60, time.Hour) {
		return
	}
	id, err := s.google.Verify(r.Context(), body.Credential, time.Now())
	if errors.Is(err, auth.ErrBadLogin) {
		writeError(w, http.StatusUnauthorized, "bad_login")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.players.SignIn(r.Context(), userFrom(r.Context()), r.UserAgent(), id)
	s.writeAuth(w, r, out, err)
}

func (s *Server) writeAuth(w http.ResponseWriter, r *http.Request, out players.Outcome, err error) {
	if err != nil {
		fail(w, r, err)
		return
	}
	if out.Token != "" {
		s.setSession(w, out.Token)
	}
	res := oapi.AuthResult{Status: oapi.AuthResultStatus(out.Status)}
	if out.Status == players.Pending {
		writeJSON(w, http.StatusOK, res)
		return
	}
	me, err := s.meOut(r.Context(), out.User)
	if err != nil {
		fail(w, r, err)
		return
	}
	res.Me = &me
	if out.Status == players.Merge {
		other, err := s.profileOut(r.Context(), out.Other)
		if err != nil {
			fail(w, r, err)
			return
		}
		res.Merge = &oapi.MergeOffer{Token: out.MergeToken, Other: other}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) MergeAccounts(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.MergeAccountsJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	nu, err := s.players.AcceptMerge(r.Context(), *u, body.Token)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeMe(w, r, &nu)
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	var body oapi.LogoutJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if c, err := r.Cookie(s.cookieName); err == nil {
		everywhere := body.Everywhere != nil && *body.Everywhere
		if err := s.players.Logout(r.Context(), userFrom(r.Context()), c.Value, everywhere); err != nil {
			fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", Domain: s.cookieDomain, MaxAge: -1,
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------- the player's own account ----------

func (s *Server) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.ProfileUpdate
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "profile:"+u.ID.String(), 30, time.Hour) {
		return
	}
	nu, err := s.players.UpdateProfile(r.Context(), *u, body.Name, body.Avatar)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeMe(w, r, &nu)
}

func (s *Server) UpdatePrefs(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.Prefs
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	// generous: a tap on the compass in a game saves it
	if !s.allow(w, r, "prefs:"+u.ID.String(), 300, time.Hour) {
		return
	}
	var p players.Prefs
	if body.Compass != nil {
		p.Compass = string(*body.Compass)
	}
	nu, err := s.players.UpdatePrefs(r.Context(), *u, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeMe(w, r, &nu)
}

func (s *Server) UnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.UnlinkIdentityJSONBody
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if err := s.players.Unlink(r.Context(), *u, body.Id); err != nil {
		fail(w, r, err)
		return
	}
	s.writeMe(w, r, u)
}

func (s *Server) GetMyProfile(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	p, err := s.profileOut(r.Context(), *u)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) ListMyStreets(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	rows, err := s.q.MyStreets(r.Context(), u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	total, err := s.streetsTotal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := oapi.MyStreets{Streets: make([]oapi.MyStreet, len(rows)), Total: total}
	for i, row := range rows {
		place := text(places.NewText(row.PlaceUz, row.PlaceRu, row.PlaceEn))
		out.Streets[i] = oapi.MyStreet{
			Name:    text(places.NewText(row.NameUz, row.NameRu, "")),
			Place:   &place,
			Rounds:  int(row.Rounds),
			Learned: row.BestKm >= 0 && row.BestKm <= places.LearnedKm,
		}
		if row.BestKm >= 0 {
			km := row.BestKm
			out.Streets[i].BestKm = &km
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- other players ----------

func (s *Server) GetProfile(w http.ResponseWriter, r *http.Request, id oapi.UserId) {
	u, err := s.q.GetUser(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.profileOut(r.Context(), u)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) ReportUser(w http.ResponseWriter, r *http.Request, id oapi.UserId) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.ReportUserJSONBody
	if !decode(r, &body) || !body.Reason.Valid() || id == u.ID {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "report:"+u.ID.String(), 20, 24*time.Hour) {
		return
	}
	target, err := s.q.GetUser(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.q.CreateReport(r.Context(), store.CreateReportParams{
		ReporterID: u.ID, UserID: target.ID, Reason: string(body.Reason), Name: target.Name,
	}); err != nil {
		fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "player reported", "user", target.ID, "name", target.Name, "reason", body.Reason)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- shapes ----------

func publicUser(u store.User) oapi.PublicUser {
	out := oapi.PublicUser{Id: u.ID, Name: u.Name, Avatar: u.Avatar, Guest: u.Guest, JoinedAt: u.CreatedAt}
	if u.BannedAt != nil {
		banned := true
		out.Banned = &banned
	}
	return out
}

func (s *Server) meOut(ctx context.Context, u store.User) (oapi.Me, error) {
	st, err := s.players.Stats(ctx, u)
	if err != nil {
		return oapi.Me{}, err
	}
	ids, err := s.q.UserIdentities(ctx, u.ID)
	if err != nil {
		return oapi.Me{}, err
	}
	me := oapi.Me{
		User:       oapi.User{Id: u.ID, Name: u.Name, Avatar: u.Avatar, Guest: u.Guest, Prefs: prefsOut(players.UserPrefs(u))},
		Stats:      oapi.Stats{Games: st.Games, Best: st.Best, AvgRound: st.AvgRound},
		Identities: make([]oapi.Identity, len(ids)),
	}
	if u.BannedAt != nil {
		banned := true
		me.User.Banned = &banned
	}
	for i, id := range ids {
		me.Identities[i] = oapi.Identity{
			Id: players.IdentityID(id), Provider: oapi.IdentityProvider(id.Provider),
			Name: id.Name, Username: id.Username, CreatedAt: id.CreatedAt,
		}
	}
	return me, nil
}

func (s *Server) profileOut(ctx context.Context, u store.User) (oapi.Profile, error) {
	st, err := s.q.ProfileStats(ctx, u.ID)
	if err != nil {
		return oapi.Profile{}, err
	}
	streets, err := s.q.StreetProgress(ctx, store.StreetProgressParams{UserID: u.ID, LearnedKm: places.LearnedKm})
	if err != nil {
		return oapi.Profile{}, err
	}
	bests, err := s.q.MapBests(ctx, u.ID)
	if err != nil {
		return oapi.Profile{}, err
	}
	recent, err := s.games.History(ctx, u.ID, nil, 0, 6)
	if err != nil {
		return oapi.Profile{}, err
	}
	total, err := s.streetsTotal(ctx)
	if err != nil {
		return oapi.Profile{}, err
	}
	p := oapi.Profile{
		User: publicUser(u),
		Stats: oapi.ProfileStats{
			Games: int(st.Games), Rounds: int(st.Rounds), Best: int(st.Best), Perfect: int(st.Perfect), Km: st.KmSum,
		},
		Streets: oapi.StreetProgress{Seen: int(streets.Seen), Learned: int(streets.Learned), Total: total},
		Maps:    make([]oapi.MapBest, len(bests)),
		Recent:  make([]oapi.GameListItem, len(recent)),
	}
	if st.Rounds > 0 {
		p.Stats.AvgRound = int(st.ScoreSum / int64(st.Rounds))
	}
	for i, b := range bests {
		p.Maps[i] = oapi.MapBest{
			Map:   oapi.MapRef{Slug: b.Slug, Name: text(places.NewText(b.NameUz, b.NameRu, b.NameEn))},
			Games: int(b.Games), Best: int(b.Total), Rounds: int(b.Rounds), GameId: b.GameID,
		}
	}
	for i, it := range recent {
		p.Recent[i] = oapi.GameListItem{
			Id: it.ID, Map: oapi.MapRef{Slug: it.MapSlug, Name: text(it.MapName)}, Status: oapi.GameStatus(it.Status),
			StartedAt: it.StartedAt, FinishedAt: it.FinishedAt, Total: it.Total, Max: it.Rounds * 5000, Rounds: it.Rounds,
		}
		if it.Hints > 0 {
			p.Recent[i].HintsUsed = &it.Hints
		}
	}
	return p, nil
}

// streetsTotal: the named streets of the street map, on the players' providers.
func (s *Server) streetsTotal(ctx context.Context) (int, error) {
	list, err := s.catalog.Official(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range list {
		if m.Pick.StreetsOnly {
			return m.Shown(s.games.Reg.Allowed(nil)).Streets, nil
		}
	}
	return 0, nil
}

// player of a game, for its page.
func (s *Server) gamePlayer(ctx context.Context, id uuid.UUID) (oapi.PublicUser, error) {
	u, err := s.q.GetUser(ctx, id)
	if err != nil {
		return oapi.PublicUser{}, err
	}
	return publicUser(u), nil
}

func prefsOut(p players.Prefs) *oapi.Prefs {
	out := &oapi.Prefs{}
	if p.Compass != "" {
		c := oapi.PrefsCompass(p.Compass)
		out.Compass = &c
	}
	return out
}
