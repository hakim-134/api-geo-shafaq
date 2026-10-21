package main

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"
)

const (
	codeLen      = 5
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I
	maxRooms     = 2000
	roomIdleTTL  = 15 * time.Minute // a room with nobody connected is removed after this
)

var errTooManyRooms = errors.New("too_many_rooms")

// Hub owns all rooms.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewHub() *Hub {
	return &Hub{rooms: map[string]*Room{}}
}

func newCode() string {
	var b strings.Builder
	for i := 0; i < codeLen; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

func (h *Hub) create(s Settings) (*Room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.rooms) >= maxRooms {
		return nil, errTooManyRooms
	}
	for {
		code := newCode()
		if _, taken := h.rooms[code]; !taken {
			r := newRoom(code, s)
			h.rooms[code] = r
			return r, nil
		}
	}
}

func (h *Hub) get(code string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rooms[strings.ToUpper(strings.TrimSpace(code))]
}

// janitor removes rooms nobody has been connected to for roomIdleTTL.
func (h *Hub) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.mu.Lock()
			for code, r := range h.rooms {
				if r.idleFor(now) > roomIdleTTL {
					delete(h.rooms, code)
				}
			}
			h.mu.Unlock()
		}
	}
}
