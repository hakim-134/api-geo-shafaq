package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeSink struct {
	mu     sync.Mutex
	msgs   [][]byte
	closed bool
}

func (f *fakeSink) queue(m []byte) { f.mu.Lock(); f.msgs = append(f.msgs, m); f.mu.Unlock() }
func (f *fakeSink) close()         { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func (f *fakeSink) last(t *testing.T) stateMsg {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.msgs) - 1; i >= 0; i-- {
		var s stateMsg
		if json.Unmarshal(f.msgs[i], &s) == nil && s.T == "state" {
			return s
		}
	}
	t.Fatal("no state message")
	return stateMsg{}
}

var tashkent = LatLon{41.3111, 69.2797}

func testSettings() Settings { return Settings{Area: "tashkent", Diff: "normal", Rounds: 2} }

func TestRoundScoreMatchesBrowser(t *testing.T) {
	s := testSettings() // scale = 4 km
	cases := []struct {
		km   float64
		hint bool
		want int
	}{
		{0, false, 5000},
		{0.03, false, 5000}, // within 1% of the scale
		{4, false, 1839},    // 5000 * e^-1
		{4, true, 1471},     // minus 20% on normal
		{40, false, 0},
	}
	for _, c := range cases {
		if got := roundScore(s, c.km, c.hint); got != c.want {
			t.Errorf("roundScore(%v km, hint=%v) = %d, want %d", c.km, c.hint, got, c.want)
		}
	}
	if got := roundScore(Settings{Area: "tashkent", Diff: "hard"}, 0.5, true); got != roundScore(Settings{Area: "tashkent", Diff: "hard"}, 0.5, false) {
		t.Errorf("hard has no hints, so a hint must not cost anything")
	}
}

func TestFullGame(t *testing.T) {
	r := newRoom("TEST1", testSettings())
	a, b := &fakeSink{}, &fakeSink{}
	host, err := r.join(a, "  Aziz  ", "")
	if err != nil {
		t.Fatal(err)
	}
	friend, _ := r.join(b, "aziz", "")
	if host.Name != "Aziz" || friend.Name != "aziz 2" {
		t.Fatalf("names: %q, %q", host.Name, friend.Name)
	}
	if err := r.start(friend); err != errNotHost {
		t.Fatalf("friend started the game: %v", err)
	}
	must(t, r.start(host))
	if err := r.setPanorama(host, 1, LatLon{53.9, 27.56}); err != errBadInput {
		t.Fatalf("a panorama in Minsk was accepted: %v", err)
	}
	must(t, r.setPanorama(host, 1, tashkent))

	s := b.last(t)
	if s.Phase != PhasePlaying || s.Pano == nil || *s.Pano != tashkent {
		t.Fatalf("friend did not get the panorama: %+v", s)
	}

	must(t, r.guess(host, &tashkent, false))
	if s := b.last(t); s.Phase != PhasePlaying || !s.Players[0].Guessed || s.RemainingMs == 0 {
		t.Fatalf("after the first guess others should get a countdown: %+v", s)
	}
	far := LatLon{41.35, 69.30}
	must(t, r.guess(friend, &far, true))

	s = a.last(t)
	if s.Phase != PhaseResult || len(s.History) != 1 {
		t.Fatalf("round did not end: %+v", s)
	}
	if g := s.History[0].Guesses[host.ID]; g.Score != 5000 {
		t.Errorf("exact guess scored %d", g.Score)
	}
	if g := s.History[0].Guesses[friend.ID]; g.Score <= 0 || g.Score >= 5000 || g.Km == nil {
		t.Errorf("friend's guess: %+v", g)
	}

	must(t, r.next(host))
	must(t, r.setPanorama(host, 2, tashkent))
	must(t, r.guess(host, nil, false)) // time ran out without a pin
	must(t, r.guess(friend, &tashkent, false))
	must(t, r.next(host))
	if s := a.last(t); s.Phase != PhaseFinal || s.Players[0].Total != 5000 {
		t.Fatalf("final: %+v", s)
	}

	// play again in the same room
	must(t, r.start(host))
	if s := a.last(t); s.Phase != PhasePicking || s.Players[0].Total != 0 || len(s.History) != 0 {
		t.Fatalf("restart: %+v", s)
	}
}

func TestPinCountsWithoutGuess(t *testing.T) {
	r := newRoom("TEST5", Settings{Area: "tashkent", Diff: "normal", Rounds: 1, Time: 60})
	a, b, c := &fakeSink{}, &fakeSink{}, &fakeSink{}
	host, _ := r.join(a, "Aziz", "")
	friend, _ := r.join(b, "Dilnoza", "")
	late, _ := r.join(c, "Bobur", "")
	must(t, r.start(host))
	if err := r.pin(friend, &tashkent, false); err != errWrongPhase {
		t.Fatalf("pin before the round: %v", err)
	}
	must(t, r.setPanorama(host, 1, tashkent))

	near := LatLon{41.32, 69.28}
	must(t, r.pin(friend, &LatLon{41.5, 69.5}, false))
	must(t, r.pin(friend, &near, true)) // the last pin wins, with the hint it was placed with
	must(t, r.guess(host, &tashkent, false))
	if err := r.pin(host, &near, false); err != errWrongPhase {
		t.Fatalf("pin after guessing: %v", err)
	}
	if s := a.last(t); s.Phase != PhasePlaying {
		t.Fatalf("a pin must not count as a guess: %s", s.Phase)
	}

	// time runs out: the friend never pressed Guess, the late player never placed a pin
	r.mu.Lock()
	r.endRound()
	r.mu.Unlock()
	s := a.last(t)
	g := s.History[0].Guesses[friend.ID]
	if g.Pos == nil || *g.Pos != near || g.Km == nil || g.Score != roundScore(r.Settings, *g.Km, true) {
		t.Errorf("friend's pin was not scored: %+v", g)
	}
	if g := s.History[0].Guesses[late.ID]; g.Pos != nil || g.Score != 0 {
		t.Errorf("player without a pin: %+v", g)
	}
}

func TestSettingsInLobby(t *testing.T) {
	r := newRoom("TEST4", testSettings())
	a, b := &fakeSink{}, &fakeSink{}
	host, _ := r.join(a, "Aziz", "")
	friend, _ := r.join(b, "Dilnoza", "")
	hard := Settings{Area: "historic", Diff: "hard", Rounds: 7, Time: 120}

	if err := r.setSettings(friend, hard); err != errNotHost {
		t.Fatalf("friend changed the settings: %v", err)
	}
	if err := r.setSettings(host, Settings{Area: "minsk", Diff: "hard", Rounds: 7}); err != errBadInput {
		t.Fatalf("unknown area accepted: %v", err)
	}
	must(t, r.setSettings(host, hard))
	if s := b.last(t); s.Settings != hard {
		t.Fatalf("friend sees %+v", s.Settings)
	}
	must(t, r.start(host))
	if err := r.setSettings(host, testSettings()); err != errWrongPhase {
		t.Fatalf("settings changed during the game: %v", err)
	}
}

func TestReconnectAndHostHandover(t *testing.T) {
	r := newRoom("TEST2", testSettings())
	a, b := &fakeSink{}, &fakeSink{}
	host, _ := r.join(a, "Aziz", "")
	friend, _ := r.join(b, "Dilnoza", "")
	must(t, r.start(host))
	must(t, r.setPanorama(host, 1, tashkent))
	must(t, r.guess(friend, &tashkent, false))

	// the host's phone drops: the round ends (everyone online has guessed) and the friend hosts
	r.disconnect(host, a)
	s := b.last(t)
	if s.HostID != friend.ID || s.Phase != PhaseResult {
		t.Fatalf("after host dropped: host=%s phase=%s", s.HostID, s.Phase)
	}

	// the old host comes back with the token and keeps the seat
	a2 := &fakeSink{}
	back, err := r.join(a2, "", host.Token)
	if err != nil || back != host {
		t.Fatalf("rejoin: %v", err)
	}
	if s := a2.last(t); len(s.Players) != 2 || !s.Players[0].Online {
		t.Fatalf("after rejoin: %+v", s.Players)
	}
}

func TestLobbyLimits(t *testing.T) {
	r := newRoom("TEST3", testSettings())
	if _, err := r.join(&fakeSink{}, "   ", ""); err != errBadName {
		t.Errorf("empty name: %v", err)
	}
	if _, err := r.join(&fakeSink{}, strings.Repeat("x", maxNameLen+1), ""); err != errBadName {
		t.Errorf("long name: %v", err)
	}
	var first *Player
	for i := 0; i < maxPlayers; i++ {
		p, err := r.join(&fakeSink{}, "P", "")
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = p
		}
	}
	if _, err := r.join(&fakeSink{}, "One more", ""); err != errFull {
		t.Errorf("13th player: %v", err)
	}
	r.leave(first)
	if len(r.players) != maxPlayers-1 || r.hostID == first.ID {
		t.Errorf("leaving the lobby should free the seat and pass the host on")
	}
}

func TestWebSocket(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, testOrigins))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	dial := func() *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	read := func(c *websocket.Conn, want string) map[string]any {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				t.Fatalf("waiting for %q: %v", want, err)
			}
			if m["t"] == want {
				return m
			}
		}
	}

	host := dial()
	defer host.Close()
	must(t, host.WriteJSON(inMsg{T: "create", Name: "Aziz", Settings: &Settings{Area: "uzbekistan", Diff: "easy", Rounds: 3}}))
	code := read(host, "welcome")["code"].(string)

	friend := dial()
	defer friend.Close()
	must(t, friend.WriteJSON(inMsg{T: "join", Code: strings.ToLower(code), Name: "Dilnoza"}))
	read(friend, "welcome")
	if n := len(read(friend, "state")["players"].([]any)); n != 2 {
		t.Fatalf("players in room: %d", n)
	}

	bad := dial()
	defer bad.Close()
	must(t, bad.WriteJSON(inMsg{T: "join", Code: "ZZZZZ", Name: "X"}))
	_ = bad.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := bad.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != 4000 || ce.Text != "no_room" {
		t.Fatalf("unknown room: %v", err)
	}
}

var testOrigins = Origins{"https://geo.shafaq.uz", "http://localhost:*"}

func TestOrigins(t *testing.T) {
	srv := httptest.NewServer(newMux(NewHub(), testOrigins))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	for origin, want := range map[string]bool{
		"":                        true, // not a browser page
		"https://geo.shafaq.uz":   true,
		"http://localhost:8130":   true,
		"http://localhost":        true,
		"http://geo.shafaq.uz":    false,
		"https://evil.example":    false,
		"http://localhost.evil":   false,
		"https://geo.shafaq.uz.x": false,
	} {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, _, err := websocket.DefaultDialer.Dial(wsURL, h)
		if got := err == nil; got != want {
			t.Errorf("ws from %q: connected=%v, want %v (%v)", origin, got, want, err)
		}
		if c != nil {
			c.Close()
		}

		req, _ := http.NewRequest("GET", srv.URL+"/api/health", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin") != ""; res.StatusCode != 200 || got != (want && origin != "") {
			t.Errorf("health from %q: %d, CORS header %q", origin, res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
		}
	}

	if res, _ := http.Get(srv.URL + "/index.html"); res.StatusCode != 404 {
		t.Errorf("the API serves no pages, got %d", res.StatusCode)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
