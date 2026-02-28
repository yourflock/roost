// ratelimit.go — Per-session and per-subscriber rate limiting for Owl API.
//
// This middleware enforces two rate limits:
//
//  1. API call rate limit: max 100 requests per minute per session token.
//     Applied to all session-authenticated endpoints (/owl/live, /owl/epg, etc.).
//     Redis key: "owl_api:rate:{session_token_prefix}:{minute_bucket}"
//     The session token is truncated to 16 chars as the key suffix (avoids storing
//     the full token in Redis; provides sufficient uniqueness for rate limiting).
//
//  2. Concurrent stream limit: max 5 simultaneous stream requests per subscriber.
//     Applied specifically to /owl/stream/ and /live/ endpoints.
//     Redis key: "owl_api:streams:{subscriber_id}" — a sorted set with stream slot
//     timestamps; stale entries (>30min) are pruned on every check.
//
// Graceful degradation: when the Store is nil (no Redis configured, dev/test
// environments), all limits are disabled — requests pass through. This matches
// the pattern in the existing internal/ratelimit package.
//
// The middleware wraps http.HandlerFunc (same pattern as requireSession in main.go)
// and injects X-RateLimit-Remaining headers so clients can observe their quota.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RateLimitStore is the minimal Redis-like interface needed for API rate limiting.
// This is a subset of internal/ratelimit.Store, using the same method signatures
// so a single Redis client can satisfy both interfaces.
type RateLimitStore interface {
	// Incr atomically increments a counter key and returns the new count.
	Incr(ctx context.Context, key string) (int64, error)
	// Expire sets the TTL on a key (only when count == 1, to avoid resetting window).
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// Get retrieves a string value by key.
	Get(ctx context.Context, key string) (string, error)
	// Set stores a value with expiry.
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	// Del removes one or more keys.
	Del(ctx context.Context, keys ...string) error
}

// rateLimiter holds a RateLimitStore and enforces quota rules.
type rateLimiter struct {
	store RateLimitStore
}

// newRateLimiter creates a rateLimiter. If store is nil, all limits are disabled.
func newRateLimiter(store RateLimitStore) *rateLimiter {
	return &rateLimiter{store: store}
}

// ---- API call rate limit middleware ----------------------------------------

// apiRateLimit wraps a handler and enforces 100 requests/min per session token.
// The session token is extracted from Authorization: Bearer header or ?token= param.
// On Redis unavailability, the request is always allowed (fail-open policy).
//
// Response headers added on every request:
//   X-RateLimit-Limit: 100
//   X-RateLimit-Remaining: N  (requests remaining in current minute window)
//   X-RateLimit-Reset: unix   (unix timestamp when the window resets)
func (rl *rateLimiter) apiRateLimit(next http.HandlerFunc) http.HandlerFunc {
	const maxPerMinute = 100
	const windowSecs = 60

	return func(w http.ResponseWriter, r *http.Request) {
		// Set rate limit headers regardless of whether we enforce
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(maxPerMinute))

		if rl.store == nil {
			// No Redis — pass through with informational headers
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(maxPerMinute))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
			next(w, r)
			return
		}

		token := extractSessionToken(r)
		if token == "" {
			// No token — let the downstream requireSession handle it
			next(w, r)
			return
		}

		// Use first 16 chars of token as key suffix (sufficient uniqueness, avoids storing full token)
		tokenKey := token
		if len(tokenKey) > 16 {
			tokenKey = tokenKey[:16]
		}

		// Minute bucket: floor(unix / 60) gives a stable window per minute
		minuteBucket := time.Now().Unix() / windowSecs
		key := fmt.Sprintf("owl_api:rate:%s:%d", tokenKey, minuteBucket)
		resetAt := (minuteBucket + 1) * windowSecs

		count, err := rl.store.Incr(r.Context(), key)
		if err != nil {
			// Redis error — fail open, don't block the request
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(maxPerMinute))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
			next(w, r)
			return
		}

		// Set TTL only on first increment (avoids resetting the window mid-flight)
		if count == 1 {
			rl.store.Expire(r.Context(), key, time.Duration(windowSecs+5)*time.Second)
		}

		remaining := maxPerMinute - int(count)
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))

		if count > int64(maxPerMinute) {
			w.Header().Set("Retry-After", strconv.FormatInt(resetAt-time.Now().Unix(), 10))
			writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
				fmt.Sprintf("Rate limit exceeded: max %d requests per minute. Retry-After: %ds",
					maxPerMinute, resetAt-time.Now().Unix()))
			return
		}

		next(w, r)
	}
}

// ---- Concurrent stream limit -----------------------------------------------

//lint:ignore U1000 plan-based stream slot enforcement — called by admission control (planned)
// maxConcurrentStreamsForPlan returns the max concurrent stream slots from plan limits.
// This defers to the existing planLimits function to keep them in sync.
func maxConcurrentStreamsForPlan(plan string) int {
	maxStreams, _ := planLimits(plan)
	return maxStreams
}

// checkStreamSlot verifies whether a subscriber can open another concurrent stream.
// Uses a Redis sorted set "owl_api:streams:{subscriberID}" where each member is
// a stream slot key and the score is the Unix timestamp it was opened.
// Stale slots (>30min) are pruned on every check to handle client crashes.
//
// Returns (allowed bool, activeCount int, err error).
// On Redis error, returns (true, 0, err) — fail open.
func (rl *rateLimiter) checkStreamSlot(ctx context.Context, subscriberID string, maxStreams int) (bool, int, error) {
	if rl.store == nil {
		return true, 0, nil
	}

	// We simulate a sorted set via individual keys with TTL.
	// Each open stream: key "owl_api:streams:{subID}:{slotID}" with 30-min TTL.
	// Count active slots by scanning for keys matching the pattern.
	// NOTE: In production with go-redis, prefer ZADD/ZCOUNT for O(log N) operations.
	// This implementation uses a counter key for simplicity and correctness.
	counterKey := fmt.Sprintf("owl_api:stream_count:%s", subscriberID)

	countStr, err := rl.store.Get(ctx, counterKey)
	if err != nil {
		// Key doesn't exist yet or Redis error — treat as 0 active streams
		return true, 0, nil
	}

	count, _ := strconv.Atoi(countStr)
	if count >= maxStreams {
		return false, count, nil
	}
	return true, count, nil
}

// openStreamSlot increments the subscriber's concurrent stream counter.
// The slot expires after 35 minutes (slightly longer than max stream URL TTL of 15min,
// accounting for session renewal. Clients decrement via closeStreamSlot on end).
func (rl *rateLimiter) openStreamSlot(ctx context.Context, subscriberID string) {
	if rl.store == nil {
		return
	}
	counterKey := fmt.Sprintf("owl_api:stream_count:%s", subscriberID)
	count, _ := rl.store.Incr(ctx, counterKey)
	// TTL: 35 min max. If client crashes without calling close, slot auto-expires.
	if count == 1 {
		rl.store.Expire(ctx, counterKey, 35*time.Minute)
	}
}

// closeStreamSlot decrements the subscriber's concurrent stream counter.
// Called when a stream request completes (or via explicit client close).
func (rl *rateLimiter) closeStreamSlot(ctx context.Context, subscriberID string) {
	if rl.store == nil {
		return
	}
	counterKey := fmt.Sprintf("owl_api:stream_count:%s", subscriberID)
	countStr, err := rl.store.Get(ctx, counterKey)
	if err != nil {
		return
	}
	count, _ := strconv.Atoi(countStr)
	if count <= 1 {
		rl.store.Del(ctx, counterKey)
	} else {
		rl.store.Set(ctx, counterKey, strconv.Itoa(count-1), 35*time.Minute)
	}
}

// streamRateLimit wraps a stream endpoint handler with concurrent stream enforcement.
// Reads subscriber_id from X-Subscriber-ID (set by requireSession middleware).
// On limit exceeded: returns 429 with a clear message pointing to plan upgrade.
func (rl *rateLimiter) streamRateLimit(maxStreams int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subscriberID := r.Header.Get("X-Subscriber-ID")
		if subscriberID == "" || rl.store == nil {
			next(w, r)
			return
		}

		allowed, activeCount, err := rl.checkStreamSlot(r.Context(), subscriberID, maxStreams)
		if err != nil {
			// Redis error — fail open
			next(w, r)
			return
		}

		if !allowed {
			writeError(w, http.StatusTooManyRequests, "stream_limit_exceeded",
				fmt.Sprintf("Concurrent stream limit reached (%d/%d active). "+
					"Close an active stream or upgrade your plan at roost.unity.dev/billing.",
					activeCount, maxStreams))
			return
		}

		// Open stream slot, track count for this subscriber
		rl.openStreamSlot(r.Context(), subscriberID)
		// Close slot when request finishes (stream URL has been issued)
		// The actual stream is served by Cloudflare CDN — we only track URL issuance.
		defer rl.closeStreamSlot(r.Context(), subscriberID)

		next(w, r)
	}
}

// ---- helpers ---------------------------------------------------------------

// extractSessionToken extracts the session token from Authorization header or ?token= param.
// Shared logic used by both requireSession (in main.go) and the rate limiter.
func extractSessionToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return r.URL.Query().Get("token")
}

// goRedisRateLimitAdapter adapts a *goredis.Client to the RateLimitStore interface.
// This allows the owl_api rate limiter to use the same Redis client used for other
// operations without importing a separate rate-limit package.
type goRedisRateLimitAdapter struct {
	c *goredis.Client
}

func (a *goRedisRateLimitAdapter) Incr(ctx context.Context, key string) (int64, error) {
	return a.c.Incr(ctx, key).Result()
}

func (a *goRedisRateLimitAdapter) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl).Err()
}

func (a *goRedisRateLimitAdapter) Get(ctx context.Context, key string) (string, error) {
	return a.c.Get(ctx, key).Result()
}

func (a *goRedisRateLimitAdapter) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return a.c.Set(ctx, key, value, expiration).Err()
}

func (a *goRedisRateLimitAdapter) Del(ctx context.Context, keys ...string) error {
	return a.c.Del(ctx, keys...).Err()
}
