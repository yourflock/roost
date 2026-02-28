// revocation.go — JWT token revocation check middleware (P22.2.002).
//
// This middleware wraps RequireAuth and additionally checks the JWT's jti
// claim against the revoked_tokens table. Results are cached in Redis for
// 60 seconds to avoid a DB round-trip on every request.
//
// Cache semantics:
//   - Cache hit "revoked"  → return 401 immediately (no DB query)
//   - Cache hit "valid"    → proceed (no DB query)
//   - Cache miss           → query DB, cache result for 60s
//
// A daily cron call to PruneRevoked() clears expired rows from the DB table.
package middleware

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RevocationStore is the minimal Redis interface needed for revocation caching.
type RevocationStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
}

// RevocationChecker checks tokens against the revocation list.
type RevocationChecker struct {
	db     *sql.DB
	cache  RevocationStore // Redis; may be nil (cache disabled)
	logger *slog.Logger
}

// NewRevocationChecker creates a checker backed by DB and optional Redis cache.
func NewRevocationChecker(db *sql.DB, cache RevocationStore, logger *slog.Logger) *RevocationChecker {
	return &RevocationChecker{db: db, cache: cache, logger: logger}
}

// cacheKeyForJTI returns the Redis key for caching a jti revocation result.
func cacheKeyForJTI(jti string) string {
	return fmt.Sprintf("revoked:jti:%s", jti)
}

// IsRevoked returns true if the given jti is in the revocation list.
// Checks Redis cache first; falls back to DB on cache miss.
// On any error, returns false (fail open) to avoid blocking legitimate traffic.
func (rc *RevocationChecker) IsRevoked(ctx context.Context, jti string) bool {
	if jti == "" {
		return false
	}

	// Check Redis cache.
	if rc.cache != nil {
		cacheKey := cacheKeyForJTI(jti)
		val, err := rc.cache.Get(ctx, cacheKey)
		if err == nil {
			// Cache hit.
			return val == "1"
		}
		// Cache miss or error — fall through to DB.
	}

	// Query DB.
	if rc.db == nil {
		return false
	}
	var exists bool
	err := rc.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM revoked_tokens
			WHERE jti = $1 AND expires_at > now()
		)
	`, jti).Scan(&exists)
	if err != nil {
		if rc.logger != nil {
			rc.logger.Warn("revocation DB check failed", "err", err)
		}
		return false // fail open
	}

	// Cache the result for 60 seconds.
	if rc.cache != nil {
		cacheVal := "0"
		if exists {
			cacheVal = "1"
		}
		_ = rc.cache.Set(ctx, cacheKeyForJTI(jti), cacheVal, 60*time.Second)
	}

	return exists
}

// RequireNotRevoked returns an HTTP middleware that rejects requests with revoked JWTs.
// It reads the jti from the Authorization: Bearer token and checks IsRevoked.
// This middleware should be applied AFTER the auth middleware has already validated
// the JWT signature and claims.
func (rc *RevocationChecker) RequireNotRevoked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jti := extractJTIFromBearer(r)
		if jti != "" && rc.IsRevoked(r.Context(), jti) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "token_revoked",
				"message": "This token has been revoked. Please sign in again.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PruneRevoked deletes expired rows from revoked_tokens.
// Call this from a daily cron goroutine.
func PruneRevoked(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	if db == nil {
		return
	}
	var deleted int
	err := db.QueryRowContext(ctx, `SELECT prune_revoked_tokens()`).Scan(&deleted)
	if err != nil {
		if logger != nil {
			logger.Warn("revocation prune failed", "err", err)
		}
		return
	}
	if deleted > 0 && logger != nil {
		logger.Info("revocation prune complete", "deleted", deleted)
	}
}

// StartRevocationPruneCron launches a background goroutine that calls PruneRevoked
// once per day. Stops when ctx is cancelled.
func StartRevocationPruneCron(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				PruneRevoked(ctx, db, logger)
			}
		}
	}()
}

// extractJTIFromBearer reads the Authorization: Bearer header and returns the
// jti claim without doing full JWT validation (already done upstream).
// Returns "" if the header is missing, malformed, or the token has no jti.
func extractJTIFromBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	tokenStr := strings.TrimSpace(parts[1])
	if tokenStr == "" {
		return ""
	}

	// JWT is three base64url segments separated by ".".
	segments := strings.Split(tokenStr, ".")
	if len(segments) != 3 {
		return ""
	}

	// Decode payload (second segment) without verifying signature (done upstream).
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return ""
	}

	var claims struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.JTI
}
