package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/httpapi/oapi"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/places"
	"github.com/hakim-134/api-geo-shafaq/internal/room"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

// Live rooms (internal/room): actions are plain requests, and each page keeps a WebSocket
// open that brings the room's state after every change.

func who(u *store.User) room.Who {
	return room.Who{ID: u.ID, Name: u.Name, Avatar: u.Avatar, Guest: u.Guest}
}

func (s *Server) CreateRoom(w http.ResponseWriter, r *http.Request) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.CreateRoom
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "room-new:"+u.ID.String(), 30, time.Hour) {
		return
	}
	rm, err := s.rooms.Create(r.Context(), who(u), body.Map, rulesIn(body.Rules))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.roomOut(r.Context(), rm.View(u.ID)))
}

func (s *Server) GetRoom(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	rm, err := s.rooms.Get(r.Context(), code)
	if err != nil {
		fail(w, r, err)
		return
	}
	id := zeroUser
	if u := userFrom(r.Context()); u != nil {
		id = u.ID
	}
	writeJSON(w, http.StatusOK, s.roomOut(r.Context(), rm.View(id)))
}

// inRoom runs a player's action on a room and answers with the room as they now see it.
func (s *Server) inRoom(w http.ResponseWriter, r *http.Request, code string, fn func(rm *room.Room, u *store.User) error) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if !s.allow(w, r, "room-act:"+u.ID.String(), 120, time.Minute) {
		return
	}
	rm, err := s.rooms.Get(r.Context(), code)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := fn(rm, u); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.roomOut(r.Context(), rm.View(u.ID)))
}

func (s *Server) SetRoomRules(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	var body oapi.RoomRules
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.SetRules(r.Context(), u.ID, body.Map, rulesIn(body.Rules))
	})
}

func (s *Server) StartRoomGame(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		if u.BannedAt != nil {
			return errBanned
		}
		return rm.Start(r.Context(), u.ID)
	})
}

func (s *Server) NextRoomRound(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	var body oapi.RoomNext
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Next(r.Context(), u.ID, body.Finish != nil && *body.Finish)
	})
}

func (s *Server) HoldRoomRound(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Hold(r.Context(), u.ID)
	})
}

func (s *Server) KickRoomPlayer(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	var body oapi.RoomTarget
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Kick(r.Context(), u.ID, body.Player)
	})
}

func (s *Server) MakeRoomHost(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	var body oapi.RoomTarget
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.MakeHost(r.Context(), u.ID, body.Player)
	})
}

func (s *Server) LeaveRoom(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	rm, err := s.rooms.Get(r.Context(), code)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := rm.Quit(r.Context(), u.ID); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RoomRoundShown(w http.ResponseWriter, r *http.Request, code oapi.RoomCode, n oapi.RoundN) {
	var body oapi.RoundShown
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Shown(r.Context(), u.ID, n, pointIn(body.Pos))
	})
}

func (s *Server) RoomRoundFailed(w http.ResponseWriter, r *http.Request, code oapi.RoomCode, n oapi.RoundN) {
	var body oapi.RoomRoundFailed
	if !decode(r, &body) || !body.Reason.Valid() {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Fail(r.Context(), u.ID, n, body.Swap, body.Reason == oapi.RoomRoundFailedReasonNotFound)
	})
}

func (s *Server) RoomHint(w http.ResponseWriter, r *http.Request, code oapi.RoomCode, n oapi.RoundN) {
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		_, err := rm.Hint(r.Context(), u.ID, n)
		return err
	})
}

func (s *Server) RoomPin(w http.ResponseWriter, r *http.Request, code oapi.RoomCode, n oapi.RoundN) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	var body oapi.GuessRequest
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	if !s.allow(w, r, "room-pin:"+u.ID.String(), 120, time.Minute) {
		return
	}
	rm, err := s.rooms.Get(r.Context(), code)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := rm.Pin(r.Context(), u.ID, n, pointIn(body.Pos)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RoomGuess(w http.ResponseWriter, r *http.Request, code oapi.RoomCode, n oapi.RoundN) {
	var body oapi.GuessRequest
	if !decode(r, &body) {
		writeError(w, http.StatusBadRequest, "bad_input")
		return
	}
	s.inRoom(w, r, code, func(rm *room.Room, u *store.User) error {
		return rm.Guess(r.Context(), u.ID, n, pointIn(body.Pos))
	})
}

// ---------- the live socket ----------

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 25 * time.Second
	wsMaxMsg     = 1024
	wsSendBuffer = 32
	// A page sends nothing; more than this many messages in wsFloodWindow closes it.
	wsFloodMsgs   = 20
	wsFloodWindow = 10 * time.Second
	// Pages of one player open in one room at once (tabs, a phone and a laptop).
	wsPerPlayer = 4
)

func (s *Server) RoomLive(w http.ResponseWriter, r *http.Request, code oapi.RoomCode) {
	u := s.player(w, r)
	if u == nil {
		return
	}
	if u.BannedAt != nil {
		writeError(w, http.StatusForbidden, "banned")
		return
	}
	if !s.allow(w, r, "room-live:"+u.ID.String(), 60, time.Minute) {
		return
	}
	rm, err := s.rooms.Get(r.Context(), code)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := rm.Check(u.ID); err != nil {
		fail(w, r, err)
		return
	}
	if !s.livePages.add(u.ID.String()+code, wsPerPlayer) {
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	defer s.livePages.done(u.ID.String() + code)
	up := websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 4096,
		// a browser always sends Origin with a WebSocket: only the game's pages may open one
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			return o == "" || s.origins.Allowed(o)
		},
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already answered
	}
	c := &wsConn{conn: conn, send: make(chan []byte, wsSendBuffer), quit: make(chan struct{})}
	go c.writeLoop()
	ctx := context.WithoutCancel(r.Context())
	if err := rm.Join(ctx, c, who(u)); err != nil {
		c.Close(err.Error())
		return
	}
	metrics.RoomConnections.Inc()
	defer metrics.RoomConnections.Dec()
	c.readLoop()
	c.Close("gone")
	rm.Drop(ctx, c)
}

// wsConn is one page's socket. Writes go through one goroutine; Close says why it ends.
type wsConn struct {
	conn   *websocket.Conn
	send   chan []byte
	quit   chan struct{}
	once   sync.Once
	reason string
}

func (c *wsConn) Send(msg []byte) {
	select {
	case c.send <- msg:
	case <-c.quit:
	default:
		c.Close("slow") // too slow to keep up: it reconnects and gets a fresh state
	}
}

func (c *wsConn) Close(reason string) {
	c.once.Do(func() {
		c.reason = reason
		close(c.quit)
	})
}

func (c *wsConn) writeLoop() {
	ping := time.NewTicker(wsPingPeriod)
	defer ping.Stop()
	defer c.conn.Close()
	write := func(kind int, msg []byte) bool {
		_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return c.conn.WriteMessage(kind, msg) == nil
	}
	for {
		select {
		case msg := <-c.send:
			if !write(websocket.TextMessage, msg) {
				c.Close("gone")
				return
			}
		case <-ping.C:
			if !write(websocket.PingMessage, nil) {
				c.Close("gone")
				return
			}
		case <-c.quit:
			c.goodbye()
			return
		}
	}
}

// goodbye tells the page why the socket ends: a restart (1012: reconnect at once), or a
// reason not to reconnect (4000, after a "bye" message).
func (c *wsConn) goodbye() {
	deadline := time.Now().Add(time.Second)
	_ = c.conn.SetWriteDeadline(deadline)
	switch c.reason {
	case "gone", "slow":
		return
	case "restart":
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseServiceRestart, "restart"), deadline)
	default:
		reason := c.reason
		msg, _ := json.Marshal(oapi.RoomMessage{T: oapi.Bye, Reason: &reason})
		_ = c.conn.WriteMessage(websocket.TextMessage, msg)
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4000, reason), deadline)
	}
}

func (c *wsConn) readLoop() {
	c.conn.SetReadLimit(wsMaxMsg)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(wsPongWait)) })
	window, count := time.Now(), 0
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
		if time.Since(window) > wsFloodWindow {
			window, count = time.Now(), 0
		}
		if count++; count > wsFloodMsgs {
			c.Close("flood")
			return
		}
	}
}

// pageCount caps the sockets one player has open in one room.
type pageCount struct {
	mu sync.Mutex
	n  map[string]int
}

func (p *pageCount) add(key string, limit int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n == nil {
		p.n = map[string]int{}
	}
	if p.n[key] >= limit {
		return false
	}
	p.n[key]++
	return true
}

func (p *pageCount) done(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n[key]--; p.n[key] <= 0 {
		delete(p.n, key)
	}
}

// ---------- the room as JSON ----------

// encodeRoom is what every page of a room gets after a change.
func (s *Server) encodeRoom(v *room.View) []byte {
	st := s.roomOut(context.Background(), v)
	b, err := json.Marshal(oapi.RoomMessage{T: oapi.State, State: &st})
	if err != nil {
		slog.Error("room state", "room", v.Code, "err", err)
	}
	return b
}

func (s *Server) roomOut(ctx context.Context, v *room.View) oapi.RoomState {
	out := oapi.RoomState{
		Code: v.Code, Ver: v.Ver, Phase: oapi.RoomPhase(v.Phase), You: v.You, Host: v.Host,
		Map: s.roomMap(ctx, v.Map, v.Rules.Providers), Rules: rulesOut(v.Rules), Games: v.Games,
		GameId: v.GameID, YourGame: v.YourGame, Max: v.Max, MaxPlayers: room.MaxPlayers,
		Players: make([]oapi.RoomPlayer, 0, len(v.Players)), Results: make([]oapi.RoomResult, 0, len(v.Results)),
	}
	if v.NextIn != nil {
		ms := int(*v.NextIn / time.Millisecond)
		out.NextMs = &ms
	}
	if v.Blocked != nil {
		b := v.Blocked.Error()
		out.Blocked = &b
	}
	for _, p := range v.Players {
		out.Players = append(out.Players, oapi.RoomPlayer{
			Id: p.ID, Name: p.Name, Avatar: p.Avatar, Guest: p.Guest, Online: p.Online, Left: p.Left,
			Total: p.Total, Guessed: p.Guessed,
		})
		if p.ID == v.You && !p.Left {
			out.Joined = true
		}
	}
	if rd := v.Round; rd != nil {
		rr := oapi.RoomRound{
			N: rd.N, Swap: rd.Swap, View: viewOut(rd.View), Shown: rd.Shown, HintsLeft: rd.HintsLeft,
			Guessed: rd.Guessed, Answered: rd.Answered, Players: rd.Players, Hints: make([]oapi.Hint, 0, len(rd.Hints)),
		}
		for _, h := range rd.Hints {
			rr.Hints = append(rr.Hints, hintOut(h))
		}
		if rd.Remaining != nil {
			ms := int(*rd.Remaining / time.Millisecond)
			rr.RemainingMs = &ms
		}
		if rd.Pin != nil {
			pin := point(*rd.Pin)
			rr.Pin = &pin
		}
		out.Round = &rr
	}
	for _, res := range v.Results {
		heading := res.Heading
		rr := oapi.RoomResult{N: res.N, Actual: point(res.Actual), Heading: &heading, Place: placeOut(res.Place), Scores: make([]oapi.RoomScore, 0, len(res.Scores))}
		for _, sc := range res.Scores {
			o := oapi.RoomScore{Player: sc.Player, Score: sc.Score, HintsUsed: sc.Hints, Km: sc.Km}
			if sc.Base > 0 {
				o.Base = &sc.Base
			}
			if sc.Guess != nil {
				g := point(*sc.Guess)
				o.Guess = &g
			}
			rr.Scores = append(rr.Scores, o)
		}
		out.Results = append(out.Results, rr)
	}
	return out
}

func placeOut(p places.Info) oapi.PlaceInfo {
	out := oapi.PlaceInfo{
		Street: textPtr(p.Street), District: textPtr(p.District), City: textPtr(p.City), Region: textPtr(p.Region),
	}
	if p.Metro != nil {
		out.Metro = &oapi.Metro{Name: text(p.Metro.Name), Km: p.Metro.Km}
	}
	return out
}

// roomMap is the room's map as its rules play it, kept a minute: every page of a room gets
// it after every change.
func (s *Server) roomMap(ctx context.Context, m *maps.Map, providers []string) oapi.MapSummary {
	key := m.Slug + "|" + strings.Join(slices.Sorted(slices.Values(providers)), ",")
	s.mapCache.mu.Lock()
	if e, ok := s.mapCache.m[key]; ok && time.Since(e.at) < time.Minute {
		s.mapCache.mu.Unlock()
		return e.v
	}
	s.mapCache.mu.Unlock()
	tree, err := s.places.Get(ctx)
	if err != nil {
		slog.WarnContext(ctx, "room map: place tree", "err", err)
	}
	v := mapSummary(m, s.games.Reg.Allowed(providers), s.games.Reg, placeView{tree: tree})
	s.mapCache.mu.Lock()
	if s.mapCache.m == nil {
		s.mapCache.m = map[string]mapCacheEntry{}
	}
	s.mapCache.m[key] = mapCacheEntry{v: v, at: time.Now()}
	s.mapCache.mu.Unlock()
	return v
}

type mapCacheEntry struct {
	v  oapi.MapSummary
	at time.Time
}

type mapCache struct {
	mu sync.Mutex
	m  map[string]mapCacheEntry
}

// roomTable is everyone's game of a room game, best first: a room game opened from history.
func (s *Server) roomTable(ctx context.Context, g *game.Game) (*oapi.RoomTable, error) {
	if g.RoomGame == nil {
		return nil, nil
	}
	rows, err := s.q.RoomGameTable(ctx, g.RoomGame)
	if err != nil {
		return nil, err
	}
	out := &oapi.RoomTable{Id: *g.RoomGame, Players: make([]oapi.RoomTableRow, 0, len(rows))}
	if g.RoomCode != nil {
		out.Code = *g.RoomCode
	}
	for _, r := range rows {
		u := store.User{ID: r.UserID, Name: r.Name, Avatar: r.Avatar, Guest: r.Guest, CreatedAt: r.CreatedAt, BannedAt: r.BannedAt}
		row := oapi.RoomTableRow{Player: publicUser(u), Game: r.ID, Total: int(r.Total), Rounds: int(r.Played)}
		if n := game.CostlyHints(r.Rules, int(r.HintsUsed)); n > 0 {
			row.Hints = &n
		}
		out.Players = append(out.Players, row)
	}
	return out, nil
}
