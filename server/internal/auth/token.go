// token.go — API token validation for Owl addon integration.
// API tokens are long-lived subscriber credentials used by Owl apps.
// Validation is cached in Redis (60s TTL) to avoid hitting Postgres on every stream request.
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TokenSubscriber holds the minimal subscriber data returned by token validation.
type TokenSubscriber struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	EmailVerified bool      `json:"email_verified"`
	Status        string    `json:"status"`
}

// ErrTokenInvalid is returned when an API token cannot be validated.
var ErrTokenInvalid = errors.New("invalid or revoked API token")

// ValidateAPIToken validates a raw Roost API token (format: roost_{64hex}).
// It hashes the token, queries the DB for a matching active record, joins
// to the subscriber, and caches the result in Redis for 60 seconds.
// On cache hit, the DB is skipped entirely. On revocation, the TTL ensures
// stale cache entries expire within 60 seconds.
func ValidateAPIToken(ctx context.Context, db *sql.DB, rawToken string) (*TokenSubscriber, error) {
	// Basic format check — tokens must start with "roost_"
	if !strings.HasPrefix(rawToken, "roost_") {
		return nil, ErrTokenInvalid
	}

	hash := HashToken(rawToken)
	cacheKey := fmt.Sprintf("token_cache:%s", hash[:16]) // use first 16 chars of hash as key suffix

	// Try Redis cache first
	if redisClient != nil {
		if cached, err := redisClient.Get(ctx, cacheKey).Result(); err == nil {
			var sub TokenSubscriber
			if json.Unmarshal([]byte(cached), &sub) == nil {
				// Update last_used_at in background (non-blocking)
				go updateTokenLastUsed(hash)
				return &sub, nil
			}
		}
	}

	// Cache miss — query DB
	query := `
		SELECT s.id, s.email, s.display_name, s.email_verified, s.status
		FROM api_tokens t
		JOIN subscribers s ON s.id = t.subscriber_id
		WHERE t.token_hash = $1
		  AND t.is_active = true
		  AND s.status = 'active'
	`

	var sub TokenSubscriber
	err := db.QueryRowContext(ctx, query, hash).Scan(
		&sub.ID, &sub.Email, &sub.DisplayName, &sub.EmailVerified, &sub.Status,
	)
	if err == sql.ErrNoRows {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("token validation query: %w", err)
	}

	// Update last_used_at
	go updateTokenLastUsed(hash)

	// Cache result — 60s TTL so revocations take effect within 1 minute
	if redisClient != nil {
		if b, err := json.Marshal(sub); err == nil {
			redisClient.Set(ctx, cacheKey, b, 60*time.Second)
		}
	}

	return &sub, nil
}

// updateTokenLastUsed fires an async DB update for last_used_at.
// Uses a fresh connection from the global pool; errors are logged but not fatal.
func updateTokenLastUsed(hash string) {
	if globalDB == nil {
		return
	}
	globalDB.Exec(
		`UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1`,
		hash,
	)
}

// RedisClient and global DB are set by the service initializer (main.go).
// Using package-level vars keeps validation callers clean.
var redisClient redisGetSetter
var globalDB *sql.DB

// redisGetSetter is the minimal Redis interface needed for token caching.
type redisGetSetter interface {
	Get(ctx context.Context, key string) interface{ Result() (string, error) }
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) interface{ Err() error }
}

// SetRedisClient wires the Redis client for token caching. Called from main.go.
func SetRedisClient(r redisGetSetter) { redisClient = r }

// SetDB wires the DB pool for token validation and last_used_at updates.
func SetDB(db *sql.DB) { globalDB = db }

// GetRedisAddr returns the Redis address from environment, with fallback.
func GetRedisAddr() string {
	if addr := os.Getenv("REDIS_URL"); addr != "" {
		return addr
	}
	return "localhost:6379"
}
