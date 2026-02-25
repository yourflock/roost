// redis_store.go — go-redis v9 adapter implementing the ratelimit.Store interface.
// Drop this file alongside ratelimit.go; nothing else needs to change.
package ratelimit

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisStore wraps a go-redis client and satisfies the Store interface.
type RedisStore struct {
	c *goredis.Client
}

// NewRedisStore creates a RedisStore from a go-redis Client.
func NewRedisStore(c *goredis.Client) *RedisStore {
	return &RedisStore{c: c}
}

func (s *RedisStore) Incr(ctx context.Context, key string) (int64, error) {
	return s.c.Incr(ctx, key).Result()
}

func (s *RedisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.c.Expire(ctx, key, ttl).Err()
}

func (s *RedisStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	return s.c.TTL(ctx, key).Result()
}

func (s *RedisStore) Del(ctx context.Context, keys ...string) error {
	return s.c.Del(ctx, keys...).Err()
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	return s.c.Get(ctx, key).Result()
}

func (s *RedisStore) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return s.c.Set(ctx, key, value, expiration).Err()
}
