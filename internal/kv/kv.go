// Package kv is Valkey (Redis-compatible): rate limits and the live rooms' snapshots.
package kv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("valkey url: %w", err)
	}
	c := redis.NewClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("valkey not reachable: %w", err)
	}
	return c, nil
}

// Limiter allows a number of events per window and key (a fixed window: simple, and good
// enough to stop floods).
type Limiter struct {
	C *redis.Client
}

// Allow counts one event. If Valkey is down it allows: a missing rate limit is better than
// a game nobody can play.
func (l Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	if l.C == nil {
		return true
	}
	k := fmt.Sprintf("rl:%s:%d", key, time.Now().Unix()/int64(window.Seconds()))
	pipe := l.C.TxPipeline()
	incr := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, window+time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("rate limiter unavailable", "err", err)
		return true
	}
	return incr.Val() <= int64(limit)
}

// Rooms keeps the live rooms' snapshots (internal/room): one key per room, room:<CODE>,
// expiring roomTTL after its last save.
type Rooms struct {
	C *redis.Client
}

const roomTTL = 2 * time.Hour

func (s Rooms) key(code string) string { return "room:" + code }

func (s Rooms) Save(ctx context.Context, code string, data []byte) error {
	return s.C.Set(ctx, s.key(code), data, roomTTL).Err()
}

func (s Rooms) Load(ctx context.Context, code string) ([]byte, error) {
	b, err := s.C.Get(ctx, s.key(code)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return b, err
}

func (s Rooms) Delete(ctx context.Context, code string) error {
	return s.C.Del(ctx, s.key(code)).Err()
}

func (s Rooms) TTL() time.Duration { return roomTTL }
