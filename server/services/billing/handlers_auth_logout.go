// handlers_auth_logout.go — Subscriber logout with JWT revocation (P22.2).
//
// POST /auth/logout revokes the caller's current JWT by its jti claim.
// Clients must discard their access and refresh tokens after calling this.
// Refresh token row is also invalidated in the DB.
package billing

import (
	"net/http"

	"github.com/unyeco/roost/internal/auth"
)

// handleLogout handles POST /auth/logout.
// Revokes the current JWT (by jti) and invalidates the subscriber's
// refresh token so they cannot silently re-authenticate.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		// Already invalid — treat as success (idempotent logout).
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "logged out"})
		return
	}

	jti := claims.ID
	subscriberID := claims.Subject

	if jti != "" {
		// Revoke this specific token by its jti.
		var expiresAt = claims.ExpiresAt.Time
		_ = auth.RevokeToken(r.Context(), s.db, jti, subscriberID, expiresAt, "logout")
	}

	// Invalidate all refresh tokens for this subscriber too.
	_, _ = s.db.ExecContext(r.Context(), `
		UPDATE auth_sessions SET is_active = FALSE
		WHERE subscriber_id = $1::uuid
	`, subscriberID)

	// Log the logout.
	_, _ = s.db.ExecContext(r.Context(), `
		INSERT INTO audit_log (actor_type, actor_id, action, resource_type, resource_id)
		VALUES ('subscriber', $1, 'auth.logout', 'subscriber', $1)
	`, subscriberID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"message": "logged out",
	})
}
