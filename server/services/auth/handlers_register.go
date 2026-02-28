// handlers_register.go — subscriber registration and email verification handlers.
// P2-T02: Registration endpoint (POST /auth/register)
// P2-T03: Email verification (GET /auth/verify-email, POST /auth/resend-verification)
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
	"github.com/unyeco/roost/internal/email"
	"github.com/unyeco/roost/internal/ratelimit"
	"golang.org/x/crypto/bcrypt"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// registerRequest is the JSON body for POST /auth/register.
type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// registerResponse is returned on successful registration.
type registerResponse struct {
	SubscriberID  string `json:"subscriber_id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Message       string `json:"message"`
}

// HandleRegister processes POST /auth/register.
// Creates a subscriber, hashes their password (bcrypt cost 12), generates an
// email verification token, and dispatches the verification email via Elastic Email.
// Rate limited: 5 registrations per IP per hour.
func HandleRegister(db *sql.DB, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		ip := ratelimit.ClientIP(r)
		if allowed, retryAfter := limiter.CheckRegistration(r.Context(), ip); !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			auth.WriteError(w, http.StatusTooManyRequests, "rate_limited",
				"Too many registration attempts. Please try again later.")
			return
		}

		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		// Validate email format
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if !emailRegex.MatchString(req.Email) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_email", "Email address is not valid")
			return
		}

		// Validate password strength (min 8 chars)
		if len(req.Password) < 8 {
			auth.WriteError(w, http.StatusBadRequest, "weak_password",
				"Password must be at least 8 characters")
			return
		}

		// Validate display name
		req.DisplayName = strings.TrimSpace(req.DisplayName)
		if len(req.DisplayName) > 100 {
			auth.WriteError(w, http.StatusBadRequest, "invalid_display_name",
				"Display name must be 100 characters or less")
			return
		}

		// Hash password — bcrypt cost 12 as specified
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Registration failed")
			return
		}

		// Insert subscriber — check for duplicate email
		var subscriberID string
		err = db.QueryRowContext(r.Context(), `
			INSERT INTO subscribers (email, password_hash, display_name, status, email_verified)
			VALUES ($1, $2, $3, 'pending', false)
			RETURNING id
		`, req.Email, string(hash), req.DisplayName).Scan(&subscriberID)

		if err != nil {
			if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
				// Privacy: use generic message, don't reveal email exists
				auth.WriteError(w, http.StatusConflict, "registration_failed",
					"Unable to create account with these details")
				return
			}
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Registration failed")
			return
		}

		// Generate email verification token (32 random bytes)
		rawToken, err := generateVerificationToken(db, subscriberID)
		if err == nil {
			// Send verification email — fire and forget (don't block registration response)
			baseURL := getBaseURL()
			verifyURL := fmt.Sprintf("%s/verify?token=%s", baseURL, rawToken)
			displayName := req.DisplayName
			if displayName == "" {
				displayName = req.Email
			}
			go email.SendVerificationEmail(req.Email, displayName, verifyURL)
		}

		auth.WriteJSON(w, http.StatusCreated, registerResponse{
			SubscriberID:  subscriberID,
			Email:         req.Email,
			EmailVerified: false,
			Message:       "Account created. Please check your email to verify your address.",
		})
	}
}

// HandleVerifyEmail processes GET /auth/verify-email?token=xxx.
// Hashes the token, looks it up, and marks the subscriber as verified and active.
func HandleVerifyEmail(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}

		rawToken := r.URL.Query().Get("token")
		if rawToken == "" {
			auth.WriteError(w, http.StatusBadRequest, "missing_token", "Verification token is required")
			return
		}

		tokenHash := auth.HashToken(rawToken)

		// Look up token — single query joining token + subscriber
		var tokenID string
		var subscriberID string
		var expiresAt time.Time
		var usedAt sql.NullTime

		err := db.QueryRowContext(r.Context(), `
			SELECT id, subscriber_id, expires_at, used_at
			FROM email_verification_tokens
			WHERE token_hash = $1
		`, tokenHash).Scan(&tokenID, &subscriberID, &expiresAt, &usedAt)

		if err == sql.ErrNoRows {
			auth.WriteError(w, http.StatusNotFound, "invalid_token", "Verification token not found")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Verification failed")
			return
		}

		if usedAt.Valid {
			auth.WriteError(w, http.StatusConflict, "token_used", "Token has already been used")
			return
		}

		if time.Now().After(expiresAt) {
			auth.WriteError(w, http.StatusGone, "token_expired",
				"Verification token has expired. Request a new one.")
			return
		}

		// Mark token used and subscriber verified — in a transaction
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Verification failed")
			return
		}
		defer tx.Rollback()

		tx.ExecContext(r.Context(), `
			UPDATE email_verification_tokens SET used_at = now() WHERE id = $1
		`, tokenID)

		tx.ExecContext(r.Context(), `
			UPDATE subscribers SET email_verified = true, status = 'active' WHERE id = $1
		`, subscriberID)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Verification failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"message":       "Email verified successfully. You can now log in.",
			"email_verified": true,
		})
	}
}

// HandleResendVerification processes POST /auth/resend-verification.
// Generates a new token and sends a new verification email.
// Rate limited: 1 per email per 5 minutes. Always returns 200 (privacy).
func HandleResendVerification(db *sql.DB, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}

		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Return 200 even on parse error (privacy)
			auth.WriteJSON(w, http.StatusOK, map[string]string{"message": "If this email is registered, a verification email will be sent."})
			return
		}

		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		// Rate limit by email
		if allowed, _ := limiter.CheckResendVerification(r.Context(), req.Email); !allowed {
			// Still return 200 — don't reveal that email is known or rate limited
			auth.WriteJSON(w, http.StatusOK, map[string]string{"message": "If this email is registered, a verification email will be sent."})
			return
		}

		// Look up subscriber (silently ignore if not found)
		var subscriberID, displayName string
		err := db.QueryRowContext(r.Context(), `
			SELECT id, COALESCE(display_name, email) FROM subscribers
			WHERE email = $1 AND email_verified = false
		`, req.Email).Scan(&subscriberID, &displayName)

		if err == nil {
			// Invalidate old tokens for this subscriber
			db.ExecContext(r.Context(), `
				UPDATE email_verification_tokens SET used_at = now()
				WHERE subscriber_id = $1 AND used_at IS NULL
			`, subscriberID)

			// Generate new token and send email
			rawToken, err := generateVerificationToken(db, subscriberID)
			if err == nil {
				baseURL := getBaseURL()
				verifyURL := fmt.Sprintf("%s/verify?token=%s", baseURL, rawToken)
				go email.SendVerificationEmail(req.Email, displayName, verifyURL)
			}
		}

		// Always return 200 regardless of outcome
		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "If this email is registered and unverified, a new verification email will be sent.",
		})
	}
}

// generateVerificationToken creates a new email verification token for a subscriber.
// Returns the raw token (to embed in the email URL). Stores only the SHA-256 hash.
func generateVerificationToken(db *sql.DB, subscriberID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	rawToken := hex.EncodeToString(b)
	tokenHash := auth.HashToken(rawToken)
	expiresAt := time.Now().Add(24 * time.Hour)

	_, err := db.Exec(`
		INSERT INTO email_verification_tokens (subscriber_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, subscriberID, tokenHash, expiresAt)
	if err != nil {
		return "", err
	}
	return rawToken, nil
}

// getBaseURL returns the Roost base URL from environment with fallback.
func getBaseURL() string {
	if u := getEnv("ROOST_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:3001"
}
