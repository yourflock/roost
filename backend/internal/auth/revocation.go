// revocation.go — JWT token revocation list (P22.2).
//
// When a subscriber signs out or changes password, we blocklist their current
// JWT's jti claim. ValidateAccessToken checks this list before accepting a token.
//
// Storage: Postgres table revoked_tokens (040_jwt_revocation.sql).
// A background goroutine prunes expired entries every hour.
// Redis caching is NOT used here — revocations must be authoritative.
package auth

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// RevokeToken adds a jti to the revocation list with the given expiry and reason.
// Called by the logout and password-change handlers.
func RevokeToken(ctx context.Context, db *sql.DB, jti, subscriberID string, expiresAt time.Time, reason string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO revoked_tokens (jti, subscriber_id, expires_at, reason)
		VALUES ($1, $2::uuid, $3, $4)
		ON CONFLICT (jti) DO NOTHING
	`, jti, subscriberID, expiresAt, reason)
	return err
}

// IsRevoked returns true if the given jti is on the revocation list.
// Returns false (allow) on DB error to avoid locking out subscribers on DB hiccup.
func IsRevoked(ctx context.Context, db *sql.DB, jti string) bool {
	if db == nil || jti == "" {
		return false
	}
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM revoked_tokens
			WHERE jti = $1 AND expires_at > now()
		)
	`, jti).Scan(&exists)
	if err != nil {
		// On DB error, fail open (don't block legit traffic).
		return false
	}
	return exists
}

// StartRevocationPruner runs a background goroutine that prunes expired
// revoked_tokens entries every hour. Call from service main().
func StartRevocationPruner(db *sql.DB, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			var deleted int
			err := db.QueryRow(`SELECT prune_revoked_tokens()`).Scan(&deleted)
			if err != nil {
				logger.Warn("revocation pruner error", "err", err)
			} else if deleted > 0 {
				logger.Info("revocation pruner", "pruned", deleted)
			}
		}
	}()
}
