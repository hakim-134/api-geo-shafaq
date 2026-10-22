// Package kv is Valkey (Redis-compatible): rate limits now, live-room snapshots and
// presence later.
package kv

import (
	"context"
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
