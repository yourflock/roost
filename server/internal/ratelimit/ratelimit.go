// Package ratelimit provides Redis-backed rate limiting for auth endpoints.
// When Redis is unavailable (nil store), all rate limits are disabled — requests pass.
// This ensures the service degrades gracefully in dev/test environments without Redis.
// All email addresses are SHA-256 hashed before use as Redis keys to avoid storing PII.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Store is the minimal interface required for rate limiting.
// In production this is implemented by go-redis; in tests by an in-memory map.
type Store interface {
	// Incr atomically increments a counter key and returns the new value.
	Incr(ctx context.Context, key string) (int64, error)
	// Expire sets the TTL on a key (only if TTL not already set by the incr).
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// TTL returns the remaining time-to-live on a key. Returns 0 or negative if expired/missing.
	TTL(ctx context.Context, key string) (time.Duration, error)
	// Del removes one or more keys.
	Del(ctx context.Context, keys ...string) error
	// Get returns the string value of a key.
	Get(ctx context.Context, key string) (string, error)
	// Set stores a value with expiry.
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
}

// Limiter performs rate limit checks against a Store.
type Limiter struct {
	store Store
}

// New creates a Limiter backed by the given Store.
// If store is nil, the Limiter is a no-op that always allows requests.
func New(store Store) *Limiter {
	return &Limiter{store: store}
}

// CheckRegistration enforces: max 5 registration attempts per IP per hour.
// Returns (allowed bool, retryAfterSecs int).
func (l *Limiter) CheckRegistration(ctx context.Context, ip string) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rate:register:%s", ip), 5, 3600)
}

// CheckLogin enforces: max 20 login attempts per IP per 15 minutes.
func (l *Limiter) CheckLogin(ctx context.Context, ip string) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rate:login:ip:%s", ip), 20, 900)
}

// ResetLoginIP resets the IP-based login counter on successful login.
func (l *Limiter) ResetLoginIP(ctx context.Context, ip string) {
	if l.store == nil {
		return
	}
	l.store.Del(ctx, fmt.Sprintf("rate:login:ip:%s", ip))
}

// CheckForgotPassword enforces: max 3 forgot-password requests per email per hour.
func (l *Limiter) CheckForgotPassword(ctx context.Context, email string) (bool, int) {
	key := fmt.Sprintf("rate:forgot:%s", hashEmail(email))
	return l.check(ctx, key, 3, 3600)
}

// CheckResendVerification enforces: max 1 resend per email per 5 minutes.
func (l *Limiter) CheckResendVerification(ctx context.Context, email string) (bool, int) {
	key := fmt.Sprintf("rate:resend:%s", hashEmail(email))
	return l.check(ctx, key, 1, 300)
}

// RecordLoginFailure records a failed login for an email and returns lockout state.
// Lockout thresholds: 5→5min, 10→30min, 15→24hr.
// Returns (isLocked bool, lockoutSeconds int, isFirstLockoutToday bool).
func (l *Limiter) RecordLoginFailure(ctx context.Context, email string) (isLocked bool, lockoutSecs int, firstLockoutToday bool) {
	if l.store == nil {
		return false, 0, false
	}

	failKey := fmt.Sprintf("lockout:email:%s:fails", hashEmail(email))
	count, _ := l.store.Incr(ctx, failKey)
	l.store.Expire(ctx, failKey, 24*time.Hour)

	switch {
	case count >= 15:
		lockoutSecs = 86400
		isLocked = true
	case count >= 10:
		lockoutSecs = 1800
		isLocked = true
	case count >= 5:
		lockoutSecs = 300
		isLocked = true
	}

	if isLocked {
		lockoutKey := fmt.Sprintf("lockout:email:%s:until", hashEmail(email))
		unlockAt := fmt.Sprintf("%d", time.Now().Add(time.Duration(lockoutSecs)*time.Second).Unix())
		l.store.Set(ctx, lockoutKey, unlockAt, time.Duration(lockoutSecs)*time.Second)

		dailyKey := fmt.Sprintf("lockout:email:%s:notified:%s", hashEmail(email), time.Now().Format("2006-01-02"))
		notified, _ := l.store.Get(ctx, dailyKey)
		firstLockoutToday = notified == ""
		if firstLockoutToday {
			l.store.Set(ctx, dailyKey, "1", 24*time.Hour)
		}
	}

	return isLocked, lockoutSecs, firstLockoutToday
}

// CheckEmailLockout checks if an email is currently locked out.
// Returns (locked bool, secondsRemaining int).
func (l *Limiter) CheckEmailLockout(ctx context.Context, email string) (bool, int) {
	if l.store == nil {
		return false, 0
	}
	lockoutKey := fmt.Sprintf("lockout:email:%s:until", hashEmail(email))
	ttl, err := l.store.TTL(ctx, lockoutKey)
	if err != nil || ttl <= 0 {
		return false, 0
	}
	return true, int(ttl.Seconds())
}

// ResetLoginEmail clears lockout state for an email on successful login.
func (l *Limiter) ResetLoginEmail(ctx context.Context, email string) {
	if l.store == nil {
		return
	}
	h := hashEmail(email)
	l.store.Del(ctx,
		fmt.Sprintf("lockout:email:%s:fails", h),
		fmt.Sprintf("lockout:email:%s:until", h),
	)
}

// CheckTOTPAttempts enforces: max 5 TOTP code attempts per minute per subscriber.
func (l *Limiter) CheckTOTPAttempts(ctx context.Context, subscriberID string) (bool, int) {
	key := fmt.Sprintf("rate:totp:%s", subscriberID)
	return l.check(ctx, key, 5, 60)
}

// ClientIP extracts the real client IP from a request, handling reverse proxy headers.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// check is the generic increment-and-check against a Redis key.
// Returns (allowed, retryAfterSecs). If store is nil, always returns (true, 0).
func (l *Limiter) check(ctx context.Context, key string, max int, ttlSecs int) (bool, int) {
	if l.store == nil {
		return true, 0
	}

	count, err := l.store.Incr(ctx, key)
	if err != nil {
		// Redis error — fail open (allow request, don't block on infra issues)
		return true, 0
	}

	if count == 1 {
		l.store.Expire(ctx, key, time.Duration(ttlSecs)*time.Second)
	}

	if count > int64(max) {
		ttl, _ := l.store.TTL(ctx, key)
		retry := int(ttl.Seconds())
		if retry < 1 {
			retry = ttlSecs
		}
		return false, retry
	}

	return true, 0
}

// hashEmail produces a 16-hex-char hash of an email for use as Redis key suffix.
// Avoids storing plaintext emails in Redis; good enough for key uniqueness.
func hashEmail(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", sum[:8])
}

// Allow checks whether the given key is within the rate limit (P22.1.004).
// rate is the maximum number of requests allowed in the given window duration.
// Uses a sliding window counter backed by Redis INCR + EXPIRE.
//
// Returns (true, nil) if the request is allowed.
// Returns (false, nil) if the limit is exceeded.
// Returns (true, err) on Redis error — fail open to avoid blocking legitimate traffic.
func (l *Limiter) Allow(ctx context.Context, key string, rate int, window time.Duration) (bool, error) {
	if l.store == nil {
		return true, nil
	}

	count, err := l.store.Incr(ctx, key)
	if err != nil {
		// Redis unavailable — fail open.
		return true, err
	}

	if count == 1 {
		// First request in this window — set expiry.
		l.store.Expire(ctx, key, window)
	}

	return count <= int64(rate), nil
}

// RateLimitConfig holds the per-endpoint rate limit settings (P22.1.004).
// These are applied by the HTTP middleware layer.
type RateLimitConfig struct {
	// Auth endpoints: login, register, forgot-password.
	AuthRate   int           // requests per window
	AuthWindow time.Duration // sliding window duration

	// API endpoints: catalog, EPG, recommendations.
	APIRate   int
	APIWindow time.Duration

	// Stream endpoints: stream URL requests, HLS segment requests.
	StreamRate   int
	StreamWindow time.Duration

	// Admin endpoints: admin panel, superowner operations.
	AdminRate   int
	AdminWindow time.Duration
}

// DefaultRateLimits returns the production rate limit configuration (P22.1.004).
//
//	Auth:   10 requests per minute
//	API:    60 requests per minute
//	Stream: 300 requests per minute
//	Admin:  30 requests per minute
func DefaultRateLimits() RateLimitConfig {
	return RateLimitConfig{
		AuthRate:   10,
		AuthWindow: time.Minute,
		APIRate:    60,
		APIWindow:  time.Minute,
		StreamRate: 300,
		StreamWindow: time.Minute,
		AdminRate:  30,
		AdminWindow: time.Minute,
	}
}

// CheckAuth enforces the auth rate limit for the given key (typically IP or email hash).
// Returns (allowed, retryAfterSecs).
func (l *Limiter) CheckAuth(ctx context.Context, key string, cfg RateLimitConfig) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rl:auth:%s", key), cfg.AuthRate, int(cfg.AuthWindow.Seconds()))
}

// CheckAPI enforces the API rate limit for the given key (typically subscriber ID or IP).
func (l *Limiter) CheckAPI(ctx context.Context, key string, cfg RateLimitConfig) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rl:api:%s", key), cfg.APIRate, int(cfg.APIWindow.Seconds()))
}

// CheckStream enforces the stream rate limit for the given key (typically subscriber ID).
func (l *Limiter) CheckStream(ctx context.Context, key string, cfg RateLimitConfig) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rl:stream:%s", key), cfg.StreamRate, int(cfg.StreamWindow.Seconds()))
}

// CheckAdmin enforces the admin rate limit for the given key (typically admin IP or ID).
func (l *Limiter) CheckAdmin(ctx context.Context, key string, cfg RateLimitConfig) (bool, int) {
	return l.check(ctx, fmt.Sprintf("rl:admin:%s", key), cfg.AdminRate, int(cfg.AdminWindow.Seconds()))
}
