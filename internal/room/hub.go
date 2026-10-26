package room

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakim-134/api-geo-shafaq/internal/game"
	"github.com/hakim-134/api-geo-shafaq/internal/maps"
	"github.com/hakim-134/api-geo-shafaq/internal/metrics"
	"github.com/hakim-134/api-geo-shafaq/internal/rules"
)

const (
	codeLen      = 5
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I (v0's)
	maxRooms     = 2000
	tickEvery    = 500 * time.Millisecond
)

// Store keeps the rooms' snapshots: Valkey on the server, a map in tests.
type Store interface {
	Save(ctx context.Context, code string, data []byte) error
	Load(ctx context.Context, code string) ([]byte, error) // nil, nil: no such room
	Delete(ctx context.Context, code string) error
	TTL() time.Duration // how long a snapshot outlives its last save
}

// Hub owns the rooms of this process.
type Hub struct {
	Games *game.Service
	Pool  *pgxpool.Pool
	Store Store
	// Encode turns a player's view into the message their pages get (the API's JSON).
	Encode func(v *View) []byte
	Now    func() time.Time

	mu    sync.Mutex
	rooms map[string]*Room
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func newCode() string {
	var b strings.Builder
	for range codeLen {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

// NormCode is a code as typed: any case, spaces around it.
func NormCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// Create opens a room on a map with rules (nil = the map's own); its creator is the host.
func (h *Hub) Create(ctx context.Context, who Who, slug string, rs *rules.Rules) (*Room, error) {
	m, checked, err := h.Games.Prepare(ctx, slug, rs)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.rooms == nil {
		h.rooms = map[string]*Room{}
	}
	if len(h.rooms) >= maxRooms {
		h.mu.Unlock()
		return nil, ErrTooManyRooms
	}
	h.mu.Unlock()
	var code string
	for {
		code = newCode()
		data, err := h.Store.Load(ctx, code)
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		_, taken := h.rooms[code]
		h.mu.Unlock()
		if data == nil && !taken {
			break
		}
	}
	now := h.now()
	r := &Room{hub: h, m: m, conns: map[Conn]uuid.UUID{}, st: state{
		Code: code, CreatedAt: now, Map: m.Slug, Rules: checked, Host: who.ID, Phase: Lobby,
		Players:  []*Player{{ID: who.ID, Name: who.Name, Avatar: who.Avatar, Guest: who.Guest, AwaySince: &now}},
		GameRows: map[uuid.UUID]uuid.UUID{}, EmptySince: &now,
	}}
	r.mu.Lock()
	r.changed(ctx)
	r.mu.Unlock()
	h.mu.Lock()
	h.rooms[code] = r
	metrics.RoomsOpen.Set(float64(len(h.rooms)))
	h.mu.Unlock()
	slog.InfoContext(ctx, "room created", "room", code, "map", m.Slug)
	return r, nil
}

// Get is a room by its code: from memory, or from its snapshot after a restart.
func (h *Hub) Get(ctx context.Context, code string) (*Room, error) {
	code = NormCode(code)
	if len(code) != codeLen {
		return nil, ErrNotFound
	}
	h.mu.Lock()
	if r, ok := h.rooms[code]; ok {
		h.mu.Unlock()
		return r, nil
	}
	h.mu.Unlock()

	data, err := h.Store.Load(ctx, code)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, ErrNotFound
	}
	r := &Room{hub: h, conns: map[Conn]uuid.UUID{}}
	if err := json.Unmarshal(data, &r.st); err != nil {
		return nil, fmt.Errorf("room %s snapshot: %w", code, err)
	}
	r.m, err = h.Games.Catalog.Get(ctx, r.st.Map)
	if errors.Is(err, maps.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Everyone's page was cut off by the restart: from now on they are away, and get
	// awayGrace to come back before a round goes on without them.
	now := h.now()
	for _, p := range r.st.Players {
		if p.AwaySince == nil {
			p.AwaySince = &now
		}
	}
	if r.st.EmptySince == nil {
		r.st.EmptySince = &now
	}
	if r.st.GameRows == nil {
		r.st.GameRows = map[uuid.UUID]uuid.UUID{}
	}
	r.lastSave = now

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms == nil {
		h.rooms = map[string]*Room{}
	}
	if other, ok := h.rooms[code]; ok {
		return other, nil // loaded by another request meanwhile
	}
	h.rooms[code] = r
	metrics.RoomsOpen.Set(float64(len(h.rooms)))
	slog.InfoContext(ctx, "room loaded", "room", code, "phase", r.st.Phase, "ver", r.st.Ver)
	return r, nil
}

func (h *Hub) forget(r *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[r.st.Code] == r {
		delete(h.rooms, r.st.Code)
	}
	metrics.RoomsOpen.Set(float64(len(h.rooms)))
}

func (h *Hub) all() []*Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		out = append(out, r)
	}
	return out
}

// Run ticks every room until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, r := range h.all() {
				go r.tick(context.WithoutCancel(ctx))
			}
		}
	}
}

// Tick runs one tick of every room now (tests).
func (h *Hub) Tick(ctx context.Context) {
	for _, r := range h.all() {
		r.tick(ctx)
	}
}

// Shutdown saves every room and closes its pages with "restart", so they reconnect to the
// next process at once.
func (h *Hub) Shutdown(ctx context.Context) {
	for _, r := range h.all() {
		r.mu.Lock()
		r.save(ctx)
		for c := range r.conns {
			c.Close("restart")
		}
		r.conns = map[Conn]uuid.UUID{}
		r.mu.Unlock()
	}
}
