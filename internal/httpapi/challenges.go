package httpapi

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/card"
	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
)

// boardTop is how many of a challenge's leaderboard a page gets; the caller's own row comes too.
const boardTop = 50

func (s *Server) CreateChallenge(w http.ResponseWriter, r *http.Request, id oapi.GameId) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "challenge:"+u.ID.String(), 60, time.Hour) {
		return
	}
	code, err := s.games.CreateChallenge(r.Context(), u.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeChallenge(w, r, http.StatusCreated, code)
}

func (s *Server) GetChallenge(w http.ResponseWriter, r *http.Request, code string) {
	s.writeChallenge(w, r, http.StatusOK, game.NormalCode(code))
}

func (s *Server) writeChallenge(w http.ResponseWriter, r *http.Request, status int, code string) {
	uid := zeroUser
	if u := userFrom(r.Context()); u != nil {
		uid = u.ID
	}
	ch, err := s.games.GetChallenge(r.Context(), uid, code)
	if err != nil {
		fail(w, r, err)
		return
	}
	pv, err := s.placeView(r, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := oapi.Challenge{
		Code: ch.Code, Map: mapSummary(ch.Map, s.games.Reg.Allowed(ch.Rules.Providers), s.games.Reg, pv),
		Rules: rulesOut(ch.Rules), Rounds: ch.Rounds, By: publicUser(ch.By), Players: ch.Players,
		Board: make([]oapi.ChallengeRow, 0, min(len(ch.Board), boardTop+1)),
	}
	for i, b := range ch.Board {
		if i >= boardTop && b.User.ID != uid {
			continue
		}
		row := oapi.ChallengeRow{Place: b.Place, Player: publicUser(b.User), Total: b.Total, Game: b.Game}
		if b.Hints > 0 {
			row.Hints = &b.Hints
		}
		out.Board = append(out.Board, row)
	}
	if ch.You != nil {
		out.You = &oapi.ChallengeYou{Game: ch.You.Game, Status: oapi.GameStatus(ch.You.Status), Total: ch.You.Total}
		if ch.You.Place > 0 {
			out.You.Place = &ch.You.Place
		}
	}
	writeJSON(w, status, out)
}

func (s *Server) PlayChallenge(w http.ResponseWriter, r *http.Request, code string) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "game:"+u.ID.String(), 120, time.Hour) {
		return
	}
	g, err := s.games.PlayChallenge(r.Context(), u.ID, game.NormalCode(code))
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

var cardPage = template.Must(template.New("card").Parse(`<!doctype html>
<html lang="uz"><head><meta charset="utf-8">
<title>{{.Title}}</title>
<meta property="og:site_name" content="Shafaq Geo">
<meta property="og:type" content="website">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Text}}">
<meta property="og:url" content="{{.URL}}">
<meta property="og:image" content="{{.Image}}">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<meta http-equiv="refresh" content="0; url={{.URL}}">
</head><body><a href="{{.URL}}">{{.Title}}</a></body></html>
`))

// GetChallengeCard is what a link preview reads: the site is a single-page app with nothing in
// its HTML, so the web server hands previews' robots this page instead (deploy/caddy/Caddyfile).
func (s *Server) GetChallengeCard(w http.ResponseWriter, r *http.Request, code string) {
	ch, err := s.games.GetChallenge(r.Context(), zeroUser, game.NormalCode(code))
	if err != nil {
		fail(w, r, err)
		return
	}
	base := s.publicURL
	if base == "" {
		base = "http://" + r.Host // the laptop
	}
	total := 0
	for _, b := range ch.Board {
		if b.User.ID == ch.By.ID {
			total = b.Total
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_ = cardPage.Execute(w, map[string]string{
		"Title": ch.By.Name + " sizni chaqirdi",
		"Text":  fmt.Sprintf("«%s»: %d raund, %s ball. Yengib ko‘ring!", ch.Map.Name.Uz, ch.Rounds, card.Group(total)),
		"URL":   s.origins.site() + "/c/" + ch.Code,
		"Image": base + "/v1/challenges/" + ch.Code + "/card.png",
	})
}

// GetChallengeCardImage draws the card. The challenger's score is fixed, so it caches long.
func (s *Server) GetChallengeCardImage(w http.ResponseWriter, r *http.Request, code string) {
	ch, err := s.games.GetChallenge(r.Context(), zeroUser, game.NormalCode(code))
	if err != nil {
		fail(w, r, err)
		return
	}
	scores, err := s.games.CreatorScores(r.Context(), ch)
	if err != nil {
		fail(w, r, err)
		return
	}
	total := 0
	for _, sc := range scores {
		total += sc
	}
	img, err := card.Draw(card.Data{
		Name:   ch.By.Name,
		Sign:   card.Sign{Kind: signKind(ch.Map.Slug), Top: ch.Map.Desc.Uz, Name: ch.Map.Name.Uz},
		Total:  total,
		Scores: scores,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(img)
}

// signKind is the map's road sign, as the game's src/lib/maps/sign.ts.
func signKind(slug string) string {
	switch {
	case slug == "historic":
		return "brown"
	case slug == "uzbekistan":
		return "green"
	case slug == "tashkent", strings.HasPrefix(slug, "place-"):
		return "white"
	}
	return "blue"
}
