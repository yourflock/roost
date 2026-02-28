// handlers_tokens.go — API token generation and management for Owl addon integration.
// P2-T06: API Token Generation & Management
package auth

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/yourflock/roost/internal/auth"
)

// tokenResponse is the full token response shown once at creation.
type tokenResponse struct {
	Token       string `json:"token"`        // Raw token — shown ONCE, never stored
	TokenPrefix string `json:"token_prefix"` // Display prefix for identification
	CreatedAt   string `json:"created_at"`
}

// tokenListItem is the safe token record returned in list views (no raw token).
type tokenListItem struct {
	ID          string  `json:"id"`
	TokenPrefix string  `json:"token_prefix"`
	IsActive    bool    `json:"is_active"`
	LastUsedAt  *string `json:"last_used_at"`
	CreatedAt   string  `json:"created_at"`
}

// HandleGenerateToken processes POST /auth/tokens.
// Generates a new API token in format roost_{64hex}. Shows raw token once.
// Deactivates any existing active tokens for the subscriber (one active at a time).
// Requires email verification.
func HandleGenerateToken(db *sql.DB) http.HandlerFunc {
	return auth.RequireVerifiedEmail(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		// Generate token: "roost_" + 32 random bytes hex = "roost_" + 64 chars = 70 chars total
		rawToken, tokenHash, err := auth.GenerateSecureToken("roost_")
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token generation failed")
			return
		}

		// Token prefix for display: first 8 chars of hex portion (after "roost_")
		tokenPrefix := rawToken[:14] // "roost_" (6) + 8 chars = "roost_abcdef12"

		tx, txErr := db.BeginTx(r.Context(), nil)
		if txErr != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token generation failed")
			return
		}
		defer tx.Rollback()

		// Deactivate existing tokens for this subscriber
		tx.ExecContext(r.Context(), `
			UPDATE api_tokens SET is_active = false WHERE subscriber_id = $1 AND is_active = true
		`, subscriberID)

		// Insert new token
		var createdAt string
		err = tx.QueryRowContext(r.Context(), `
			INSERT INTO api_tokens (subscriber_id, token_hash, token_prefix, is_active)
			VALUES ($1, $2, $3, true)
			RETURNING created_at::text
		`, subscriberID, tokenHash, tokenPrefix).Scan(&createdAt)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token generation failed")
			return
		}

		// Log token generation to audit trail
		tx.ExecContext(r.Context(), `
			INSERT INTO audit_log (subscriber_id, action, metadata)
			VALUES ($1, 'api_token_generated', $2)
		`, subscriberID, `{"prefix":"`+tokenPrefix+`"}`)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token generation failed")
			return
		}

		// Return raw token — shown ONCE, never stored, subscriber must copy it
		auth.WriteJSON(w, http.StatusCreated, tokenResponse{
			Token:       rawToken,
			TokenPrefix: tokenPrefix,
			CreatedAt:   createdAt,
		})
	}))
}

// HandleListTokens processes GET /auth/tokens.
// Returns all tokens for the authenticated subscriber — prefix only, never raw tokens.
func HandleListTokens(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		rows, err := db.QueryContext(r.Context(), `
			SELECT id, token_prefix, is_active,
			       to_char(last_used_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			       created_at::text
			FROM api_tokens
			WHERE subscriber_id = $1
			ORDER BY created_at DESC
		`, subscriberID)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Failed to list tokens")
			return
		}
		defer rows.Close()

		var tokens []tokenListItem
		for rows.Next() {
			var t tokenListItem
			var lastUsed sql.NullString
			rows.Scan(&t.ID, &t.TokenPrefix, &t.IsActive, &lastUsed, &t.CreatedAt)
			if lastUsed.Valid {
				t.LastUsedAt = &lastUsed.String
			}
			tokens = append(tokens, t)
		}

		if tokens == nil {
			tokens = []tokenListItem{}
		}

		auth.WriteJSON(w, http.StatusOK, map[string]interface{}{"tokens": tokens})
	}))
}

// HandleRevokeToken processes DELETE /auth/tokens/:id.
// Deactivates the specified token. Only the owning subscriber can revoke.
func HandleRevokeToken(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		// Extract token ID from URL path: /auth/tokens/{id}
		tokenID := strings.TrimPrefix(r.URL.Path, "/auth/tokens/")
		if tokenID == "" || tokenID == r.URL.Path {
			auth.WriteError(w, http.StatusBadRequest, "missing_id", "Token ID required in path")
			return
		}

		// Deactivate — only if owned by this subscriber
		result, err := db.ExecContext(r.Context(), `
			UPDATE api_tokens SET is_active = false
			WHERE id = $1 AND subscriber_id = $2
		`, tokenID, subscriberID)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			auth.WriteError(w, http.StatusNotFound, "not_found",
				"Token not found or you don't have permission to revoke it")
			return
		}

		// Log revocation
		db.ExecContext(r.Context(), `
			INSERT INTO audit_log (subscriber_id, action, metadata)
			VALUES ($1, 'api_token_revoked', $2)
		`, subscriberID, `{"token_id":"`+tokenID+`"}`)

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Token revoked successfully.",
		})
	}))
}
