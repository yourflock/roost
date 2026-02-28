// handlers_login.go — login, token refresh, password reset handlers.
// P2-T04: Login & Password Reset
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
	"github.com/unyeco/roost/internal/email"
	"github.com/unyeco/roost/internal/ratelimit"
	"golang.org/x/crypto/bcrypt"
)

// loginRequest is the JSON body for POST /auth/login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponse is the full token response on successful authentication.
// If the subscriber has 2FA enabled, requires_2fa is true and only temp_token is returned.
type loginResponse struct {
	AccessToken  string           `json:"access_token,omitempty"`
	RefreshToken string           `json:"refresh_token,omitempty"`
	Requires2FA  bool             `json:"requires_2fa,omitempty"`
	TempToken    string           `json:"temp_token,omitempty"`
	Subscriber   *subscriberInfo  `json:"subscriber,omitempty"`
}

// subscriberInfo is the safe subset of subscriber data returned to clients.
type subscriberInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	EmailVerified bool   `json:"email_verified"`
	Status        string `json:"status"`
}

// HandleLogin processes POST /auth/login.
// Validates credentials, enforces rate limiting and lockout, handles 2FA redirect,
// and returns access + refresh tokens on success.
func HandleLogin(db *sql.DB, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		ip := ratelimit.ClientIP(r)

		// Per-IP rate limit
		if allowed, retryAfter := limiter.CheckLogin(r.Context(), ip); !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			auth.WriteError(w, http.StatusTooManyRequests, "rate_limited",
				"Too many login attempts from this IP. Please try again later.")
			return
		}

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		// Per-email lockout check
		if locked, retryAfter := limiter.CheckEmailLockout(r.Context(), req.Email); locked {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			auth.WriteError(w, http.StatusTooManyRequests, "account_temporarily_locked",
				fmt.Sprintf("Account temporarily locked. Try again in %d seconds.", retryAfter))
			return
		}

		// Fetch subscriber — use constant-time comparison to prevent timing attacks
		var sub struct {
			ID            string
			PasswordHash  string
			DisplayName   string
			EmailVerified bool
			Status        string
			TOTPEnabled   bool
		}

		err := db.QueryRowContext(r.Context(), `
			SELECT id, password_hash, COALESCE(display_name,''), email_verified, status, totp_enabled
			FROM subscribers WHERE email = $1
		`, req.Email).Scan(
			&sub.ID, &sub.PasswordHash, &sub.DisplayName,
			&sub.EmailVerified, &sub.Status, &sub.TOTPEnabled,
		)

		// Perform bcrypt comparison even on user-not-found to prevent timing attacks.
		// Use a dummy hash when user not found.
		dummyHash := "$2a$12$invalidhashfortimingattackprevention1234567890abcdef"
		hashToCheck := sub.PasswordHash
		if err == sql.ErrNoRows || hashToCheck == "" {
			hashToCheck = dummyHash
		}

		bcryptErr := bcrypt.CompareHashAndPassword([]byte(hashToCheck), []byte(req.Password))

		if err == sql.ErrNoRows || bcryptErr != nil {
			// Record failure for lockout tracking
			isLocked, lockoutSecs, firstLockout := limiter.RecordLoginFailure(r.Context(), req.Email)
			if isLocked && firstLockout && err == nil {
				// Send lockout notification email (non-blocking)
				displayName := sub.DisplayName
				if displayName == "" {
					displayName = req.Email
				}
				go email.SendLockoutNotificationEmail(req.Email, displayName, lockoutSecs/60)
			}

			// Generic error — never reveal which field (email/password) is wrong
			auth.WriteError(w, http.StatusUnauthorized, "invalid_credentials",
				"Invalid email or password")
			return
		}

		// Check account status
		switch sub.Status {
		case "suspended":
			auth.WriteError(w, http.StatusForbidden, "account_suspended",
				"Your account has been suspended. Contact support.")
			return
		case "cancelled":
			auth.WriteError(w, http.StatusForbidden, "account_cancelled",
				"This account has been closed.")
			return
		}

		// Successful authentication — reset rate limit counters
		limiter.ResetLoginIP(r.Context(), ip)
		limiter.ResetLoginEmail(r.Context(), req.Email)

		// 2FA check — if enabled, return temp_token instead of full tokens
		if sub.TOTPEnabled {
			tempToken, err := generateTempToken(db, sub.ID)
			if err != nil {
				auth.WriteError(w, http.StatusInternalServerError, "server_error", "Login failed")
				return
			}
			auth.WriteJSON(w, http.StatusOK, loginResponse{
				Requires2FA: true,
				TempToken:   tempToken,
			})
			return
		}

		// Issue tokens
		subUUID, _ := parseUUID(sub.ID)
		accessToken, err := auth.GenerateAccessToken(subUUID, sub.EmailVerified)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Login failed")
			return
		}

		rawRefresh, refreshHash, err := auth.GenerateRefreshToken()
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Login failed")
			return
		}

		// Store refresh token (server-side)
		_, err = db.ExecContext(r.Context(), `
			INSERT INTO refresh_tokens (subscriber_id, token_hash, expires_at)
			VALUES ($1, $2, $3)
		`, sub.ID, refreshHash, time.Now().Add(7*24*time.Hour))
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Login failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, loginResponse{
			AccessToken:  accessToken,
			RefreshToken: rawRefresh,
			Subscriber: &subscriberInfo{
				ID:            sub.ID,
				Email:         req.Email,
				DisplayName:   sub.DisplayName,
				EmailVerified: sub.EmailVerified,
				Status:        sub.Status,
			},
		})
	}
}

// HandleRefresh processes POST /auth/refresh.
// Validates a refresh token, issues a new access token, and rotates the refresh token.
func HandleRefresh(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		if req.RefreshToken == "" {
			auth.WriteError(w, http.StatusBadRequest, "missing_token", "refresh_token required")
			return
		}

		tokenHash := auth.HashToken(req.RefreshToken)

		// Look up refresh token
		var tokenID, subscriberID string
		var expiresAt time.Time
		var revokedAt sql.NullTime

		err := db.QueryRowContext(r.Context(), `
			SELECT id, subscriber_id, expires_at, revoked_at
			FROM refresh_tokens
			WHERE token_hash = $1
		`, tokenHash).Scan(&tokenID, &subscriberID, &expiresAt, &revokedAt)

		if err == sql.ErrNoRows || revokedAt.Valid {
			auth.WriteError(w, http.StatusUnauthorized, "invalid_token", "Refresh token is invalid or revoked")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token refresh failed")
			return
		}
		if time.Now().After(expiresAt) {
			auth.WriteError(w, http.StatusUnauthorized, "token_expired", "Refresh token has expired")
			return
		}

		// Fetch current subscriber state
		var emailVerified bool
		db.QueryRowContext(r.Context(), `SELECT email_verified FROM subscribers WHERE id = $1`, subscriberID).
			Scan(&emailVerified)

		// Rotate refresh token: revoke old, issue new
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token refresh failed")
			return
		}
		defer tx.Rollback()

		tx.ExecContext(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, tokenID)

		rawRefresh, refreshHash, err := auth.GenerateRefreshToken()
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token refresh failed")
			return
		}

		tx.ExecContext(r.Context(), `
			INSERT INTO refresh_tokens (subscriber_id, token_hash, expires_at)
			VALUES ($1, $2, $3)
		`, subscriberID, refreshHash, time.Now().Add(7*24*time.Hour))

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token refresh failed")
			return
		}

		subUUID, _ := parseUUID(subscriberID)
		accessToken, err := auth.GenerateAccessToken(subUUID, emailVerified)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Token refresh failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"access_token":  accessToken,
			"refresh_token": rawRefresh,
		})
	}
}

// HandleForgotPassword processes POST /auth/forgot-password.
// Always returns 200 to avoid revealing whether an email is registered.
// Rate limited: 3 per email per hour.
func HandleForgotPassword(db *sql.DB, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		var req struct {
			Email string `json:"email"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		successMsg := map[string]string{
			"message": "If this email is registered, a password reset link will be sent.",
		}

		// Rate limit silently
		if allowed, _ := limiter.CheckForgotPassword(r.Context(), req.Email); !allowed {
			auth.WriteJSON(w, http.StatusOK, successMsg)
			return
		}

		// Look up subscriber
		var subscriberID, displayName string
		err := db.QueryRowContext(r.Context(), `
			SELECT id, COALESCE(display_name, email) FROM subscribers
			WHERE email = $1 AND status IN ('active', 'pending')
		`, req.Email).Scan(&subscriberID, &displayName)

		if err == nil {
			// Generate and store reset token (1 hour expiry)
			b := make([]byte, 32)
			rand.Read(b)
			rawToken := hex.EncodeToString(b)
			tokenHash := auth.HashToken(rawToken)

			db.ExecContext(r.Context(), `
				INSERT INTO password_reset_tokens (subscriber_id, token_hash, expires_at)
				VALUES ($1, $2, $3)
			`, subscriberID, tokenHash, time.Now().Add(time.Hour))

			baseURL := getBaseURL()
			resetURL := fmt.Sprintf("%s/reset-password?token=%s", baseURL, rawToken)
			go email.SendPasswordResetEmail(req.Email, displayName, resetURL)
		}

		auth.WriteJSON(w, http.StatusOK, successMsg)
	}
}

// HandleResetPassword processes POST /auth/reset-password.
// Validates the reset token, updates the password, and invalidates all sessions.
func HandleResetPassword(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		var req struct {
			Token       string `json:"token"`
			NewPassword string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		if req.NewPassword == "" || len(req.NewPassword) < 8 {
			auth.WriteError(w, http.StatusBadRequest, "weak_password",
				"Password must be at least 8 characters")
			return
		}

		tokenHash := auth.HashToken(req.Token)

		var tokenID, subscriberID string
		var expiresAt time.Time
		var usedAt sql.NullTime

		err := db.QueryRowContext(r.Context(), `
			SELECT id, subscriber_id, expires_at, used_at
			FROM password_reset_tokens WHERE token_hash = $1
		`, tokenHash).Scan(&tokenID, &subscriberID, &expiresAt, &usedAt)

		if err == sql.ErrNoRows {
			auth.WriteError(w, http.StatusNotFound, "invalid_token", "Reset token not found")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Password reset failed")
			return
		}
		if usedAt.Valid {
			auth.WriteError(w, http.StatusConflict, "token_used", "Token has already been used")
			return
		}
		if time.Now().After(expiresAt) {
			auth.WriteError(w, http.StatusGone, "token_expired", "Reset token has expired")
			return
		}

		// Hash new password
		newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Password reset failed")
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Password reset failed")
			return
		}
		defer tx.Rollback()

		// Update password
		tx.ExecContext(r.Context(), `UPDATE subscribers SET password_hash = $1 WHERE id = $2`, string(newHash), subscriberID)

		// Mark token used
		tx.ExecContext(r.Context(), `UPDATE password_reset_tokens SET used_at = now() WHERE id = $1`, tokenID)

		// Revoke ALL refresh tokens for this subscriber (force re-login everywhere)
		tx.ExecContext(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE subscriber_id = $1 AND revoked_at IS NULL`, subscriberID)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Password reset failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Password updated successfully. All previous sessions have been signed out.",
		})
	}
}
