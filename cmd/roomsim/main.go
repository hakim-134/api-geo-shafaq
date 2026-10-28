// Command roomsim plays a live room with many players against a running API, the way the
// game's pages do: a WebSocket each that reconnects by itself, actions retried while the
// server is away. Run it, redeploy the API meanwhile, and the room should simply finish
// (PLAN 5: "12 phones play a room while the API is redeployed, and nobody notices").
// Every other result goes on by itself after its ring, the others by the host's "Hozir"; a
// third of the players take a hint, and their scores must show what the hint cost.
// Plays demo panoramas only, never Yandex.
//
//	go run ./cmd/roomsim                                   # 12 players, 3 rounds of 30 s
//	go run ./cmd/roomsim -players 4 -rounds 5 -time 20
//	go run ./cmd/roomsim & sleep 20; make lcl; wait        # with a redeploy in the middle
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	api     = flag.String("api", "http://localhost:8131", "the API")
	origin  = flag.String("origin", "http://localhost:8130", "the game's address, sent as Origin")
	mapSlug = flag.String("map", "tashkent-streets", "the map")
	players = flag.Int("players", 12, "players in the room")
	rounds  = flag.Int("rounds", 3, "rounds")
	timeSec = flag.Int("time", 30, "seconds per round")
	timeout = flag.Duration("timeout", 10*time.Minute, "give up after")
)

type state struct {
	Code     string `json:"code"`
	Ver      int64  `json:"ver"`
	Phase    string `json:"phase"`
	NextMs   *int   `json:"nextMs"`
	Host     string `json:"host"`
	You      string `json:"you"`
	YourGame string `json:"yourGame"`
	Round    *struct {
		N        int  `json:"n"`
		Swap     int  `json:"swap"`
		Shown    bool `json:"shown"`
		Guessed  bool `json:"guessed"`
		Left     int  `json:"hintsLeft"`
		Answered int  `json:"answered"`
		Players  int  `json:"players"`
	} `json:"round"`
	Results []struct {
		N      int `json:"n"`
		Scores []struct {
			Player string `json:"player"`
			Score  int    `json:"score"`
			Base   *int   `json:"base"`
			Hints  int    `json:"hintsUsed"`
		} `json:"scores"`
	} `json:"results"`
	Players []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
		Total  int    `json:"total"`
	} `json:"players"`
}

type bot struct {
	i      int
	client *http.Client
	cookie string

	mu         sync.Mutex
	st         state
	reconnects int
	changed    chan struct{}
}

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	bots := make([]*bot, *players)
	for i := range bots {
		b, err := newBot(ctx, i)
		if err != nil {
			return fmt.Errorf("player %d: %w", i, err)
		}
		bots[i] = b
	}
	rules := map[string]any{
		"rounds": *rounds, "timeSec": *timeSec, "movement": "move", "compass": true,
		"hints":     map[string]any{"count": 1, "per": "round", "kind": "district", "cost": 0.2},
		"providers": []string{"demo"}, "scoreScale": 1, "panoLabels": false, "mapLabels": true,
	}
	var st state
	if err := bots[0].post(ctx, "/v1/rooms", map[string]any{"map": *mapSlug, "rules": rules}, &st); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	code := st.Code
	fmt.Printf("room %s: %d players, %d rounds of %d s on %s\n", code, *players, *rounds, *timeSec, *mapSlug)

	var wg sync.WaitGroup
	errs := make(chan error, len(bots))
	for _, b := range bots {
		go b.live(ctx, code)
	}
	for _, b := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.play(ctx, code); err != nil {
				errs <- fmt.Errorf("player %d: %w", b.i, err)
			}
		}()
	}
	// everyone in, then the host starts
	if err := waitFor(ctx, bots[0], func(s state) bool { return countOnline(s) == len(bots) }); err != nil {
		return fmt.Errorf("waiting for everyone to join: %w", err)
	}
	if err := bots[0].postRetry(ctx, "/v1/rooms/"+code+"/start", nil); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}

	// every player's game is saved and finished
	reconnects := 0
	for _, b := range bots {
		s := b.state()
		reconnects += b.reconnects
		if s.YourGame == "" {
			return fmt.Errorf("player %d has no saved game", b.i)
		}
		var g struct {
			Status string `json:"status"`
			Total  int    `json:"total"`
			Rounds []any  `json:"rounds"`
			Room   *struct {
				Players []any `json:"players"`
			} `json:"room"`
		}
		if err := b.get(ctx, "/v1/games/"+s.YourGame, &g); err != nil {
			return fmt.Errorf("player %d's game: %w", b.i, err)
		}
		if g.Status != "finished" || len(g.Rounds) != *rounds || g.Room == nil || len(g.Room.Players) != len(bots) {
			return fmt.Errorf("player %d's game: %s, %d rounds, room %v", b.i, g.Status, len(g.Rounds), g.Room)
		}
	}
	s := bots[0].state()
	fmt.Printf("OK: room %s finished, %d rounds, every player's game saved; %d reconnects in all\n", code, len(s.Results), reconnects)
	for _, p := range s.Players {
		fmt.Printf("  %-10s %6d\n", p.Name, p.Total)
	}
	return nil
}

func countOnline(s state) int {
	n := 0
	for _, p := range s.Players {
		if p.Online {
			n++
		}
	}
	return n
}

func newBot(ctx context.Context, i int) (*bot, error) {
	jar, _ := cookiejar.New(nil)
	b := &bot{i: i, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}, changed: make(chan struct{}, 1)}
	if err := b.postRetry(ctx, "/v1/session", nil); err != nil {
		return nil, err
	}
	u, _ := url.Parse(*api)
	for _, c := range jar.Cookies(u) {
		b.cookie = c.Name + "=" + c.Value
	}
	return b, nil
}

func (b *bot) state() state {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st
}

// take keeps the newest state, as the game does.
func (b *bot) take(s state) {
	b.mu.Lock()
	if s.Ver >= b.st.Ver {
		b.st = s
	}
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

// live keeps the room's socket open, reconnecting after a drop or a restart.
func (b *bot) live(ctx context.Context, code string) {
	wsURL := strings.Replace(*api, "http", "ws", 1) + "/v1/rooms/" + code + "/live"
	h := http.Header{"Cookie": {b.cookie}, "Origin": {*origin}}
	wait := 250 * time.Millisecond
	first := true
	for ctx.Err() == nil {
		conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, h)
		if err != nil {
			if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				fmt.Printf("player %d: the room refused: %d\n", b.i, resp.StatusCode)
				return
			}
			time.Sleep(wait)
			wait = min(wait*2, 2*time.Second)
			continue
		}
		if !first {
			b.mu.Lock()
			b.reconnects++
			b.mu.Unlock()
		}
		first, wait = false, 250*time.Millisecond
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var m struct {
				T      string `json:"t"`
				State  *state `json:"state"`
				Reason string `json:"reason"`
			}
			if json.Unmarshal(data, &m) == nil {
				if m.T == "bye" {
					fmt.Printf("player %d: bye (%s)\n", b.i, m.Reason)
					conn.Close()
					return
				}
				if m.State != nil {
					b.take(*m.State)
				}
			}
		}
		conn.Close()
	}
}

// play answers every round like a person: opens it, thinks a few seconds, guesses
// somewhere in Tashkent. The host moves on after each result.
func (b *bot) play(ctx context.Context, code string) error {
	room := "/v1/rooms/" + code
	shown, guessed, nexted := map[int]bool{}, map[int]bool{}, map[int]bool{}
	checked := map[int]bool{}
	var think time.Time
	var resultAt time.Time
	for {
		s := b.state()
		switch {
		case s.Phase == "final":
			return nil
		case s.Phase == "playing" && s.Round != nil:
			n := s.Round.N
			if !shown[n] {
				if err := b.postRetry(ctx, fmt.Sprintf("%s/rounds/%d/shown", room, n), map[string]any{}); err != nil {
					return err
				}
				shown[n] = true
				think = time.Now().Add(time.Duration(3+rand.IntN(8)) * time.Second)
			}
			if !guessed[n] && !s.Round.Guessed && time.Now().After(think) {
				if b.i%3 == 1 && s.Round.Left > 0 {
					if err := b.postRetry(ctx, fmt.Sprintf("%s/rounds/%d/hint", room, n), nil); err != nil {
						return err
					}
				}
				pos := map[string]float64{"lat": 41.25 + rand.Float64()*0.12, "lon": 69.17 + rand.Float64()*0.2}
				if err := b.postRetry(ctx, fmt.Sprintf("%s/rounds/%d/guess", room, n), map[string]any{"pos": pos}); err != nil {
					return err
				}
				guessed[n] = true
			}
		case s.Phase == "result":
			n := len(s.Results)
			if !checked[n] {
				checked[n] = true
				resultAt = time.Now()
				if s.NextMs == nil {
					return fmt.Errorf("result %d: no ring (nextMs)", n)
				}
				for _, sc := range s.Results[n-1].Scores {
					if sc.Hints > 0 && sc.Score > 0 && (sc.Base == nil || *sc.Base <= sc.Score) {
						return fmt.Errorf("result %d: a hint's cost not shown: score %d, base %v", n, sc.Score, sc.Base)
					}
					if sc.Hints == 0 && sc.Base != nil {
						return fmt.Errorf("result %d: a base without a hint", n)
					}
				}
			}
			// odd results: the ring runs out by itself (13 s, a little more with a restart);
			// even ones: the host's "Hozir"
			if n%2 == 1 && time.Since(resultAt) > 40*time.Second {
				return fmt.Errorf("result %d did not go on by itself", n)
			}
			if n%2 == 0 && s.Host == s.You && !nexted[n] {
				time.Sleep(1500 * time.Millisecond)
				if err := b.postRetry(ctx, room+"/next", map[string]any{}); err != nil {
					return err
				}
				nexted[n] = true
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out in phase %s", s.Phase)
		case <-b.changed:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func waitFor(ctx context.Context, b *bot, ok func(state) bool) error {
	for !ok(b.state()) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.changed:
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}

var errAway = errors.New("server away")

// postRetry is a page's action: tried again while the server is away (a redeploy), not
// after a real answer. wrong_phase is a normal answer (the round moved on meanwhile).
func (b *bot) postRetry(ctx context.Context, path string, body any) error {
	for {
		var st state
		err := b.post(ctx, path, body, &st)
		if err == nil {
			if st.Code != "" {
				b.take(st)
			}
			return nil
		}
		if strings.Contains(err.Error(), "wrong_phase") {
			return nil
		}
		if !errors.Is(err, errAway) {
			return fmt.Errorf("%s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (b *bot) post(ctx context.Context, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, *api+path, r)
	req.Header.Set("Origin", *origin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return b.do(req, out)
}

func (b *bot) get(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, *api+path, nil)
	return b.do(req, out)
}

func (b *bot) do(req *http.Request, out any) error {
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errAway, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return fmt.Errorf("%w: %d %s", errAway, resp.StatusCode, data)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%d %s", resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
