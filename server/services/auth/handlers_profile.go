// handlers_profile.go — subscriber profile management handlers.
// P2-T05: Profile Management
package auth

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
	"github.com/yourflock/roost/internal/email"
)

// htmlTagRegex detects HTML tags for XSS prevention in display name.
var htmlTagRegex = regexp.MustCompile(`<[^>]+>`)

// HandleProfile processes GET /auth/profile.
// Returns the authenticated subscriber's profile + subscription summary.
func HandleProfile(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		var profile struct {
			ID            string       `json:"id"`
			Email         string       `json:"email"`
			DisplayName   string       `json:"display_name"`
			EmailVerified bool         `json:"email_verified"`
			Status        string       `json:"status"`
			CreatedAt     time.Time    `json:"created_at"`
			Subscription  interface{}  `json:"subscription"`
		}

		err := db.QueryRowContext(r.Context(), `
			SELECT id, email, COALESCE(display_name,''), email_verified, status, created_at
			FROM subscribers WHERE id = $1
		`, subscriberID).Scan(
			&profile.ID, &profile.Email, &profile.DisplayName,
			&profile.EmailVerified, &profile.Status, &profile.CreatedAt,
		)
		if err == sql.ErrNoRows {
			auth.WriteError(w, http.StatusNotFound, "not_found", "Subscriber not found")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Failed to fetch profile")
			return
		}

		// Fetch subscription summary (may not exist yet)
		var subSummary struct {
			PlanName    string     `json:"plan_name"`
			Status      string     `json:"status"`
			RenewalDate *time.Time `json:"renewal_date"`
		}

		err = db.QueryRowContext(r.Context(), `
			SELECT sp.name, s.status, s.current_period_end
			FROM subscriptions s
			JOIN subscription_plans sp ON sp.id = s.plan_id
			WHERE s.subscriber_id = $1
			ORDER BY s.created_at DESC LIMIT 1
		`, subscriberID).Scan(&subSummary.PlanName, &subSummary.Status, &subSummary.RenewalDate)

		if err == nil {
			profile.Subscription = subSummary
		} else {
			profile.Subscription = nil
		}

		auth.WriteJSON(w, http.StatusOK, profile)
	}))
}

// HandleUpdateProfile processes PATCH /auth/profile.
// Allows updating display_name; email change triggers re-verification.
func HandleUpdateProfile(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		var req struct {
			DisplayName *string `json:"display_name"`
			Email       *string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		if req.DisplayName == nil && req.Email == nil {
			auth.WriteError(w, http.StatusBadRequest, "no_fields", "Provide at least one field to update")
			return
		}

		if req.DisplayName != nil {
			name := strings.TrimSpace(*req.DisplayName)
			if len(name) > 100 {
				auth.WriteError(w, http.StatusBadRequest, "invalid_display_name",
					"Display name must be 100 characters or less")
				return
			}
			// Strip HTML tags
			if htmlTagRegex.MatchString(name) {
				auth.WriteError(w, http.StatusBadRequest, "invalid_display_name",
					"Display name must not contain HTML")
				return
			}

			_, err := db.ExecContext(r.Context(), `
				UPDATE subscribers SET display_name = $1 WHERE id = $2
			`, name, subscriberID)
			if err != nil {
				auth.WriteError(w, http.StatusInternalServerError, "server_error", "Update failed")
				return
			}
		}

		if req.Email != nil {
			newEmail := strings.ToLower(strings.TrimSpace(*req.Email))
			if !emailRegex.MatchString(newEmail) {
				auth.WriteError(w, http.StatusBadRequest, "invalid_email", "Email address is not valid")
				return
			}

			// Fetch current subscriber info for sending verification
			var currentEmail, displayName string
			db.QueryRowContext(r.Context(), `
				SELECT email, COALESCE(display_name, email) FROM subscribers WHERE id = $1
			`, subscriberID).Scan(&currentEmail, &displayName)

			if newEmail == currentEmail {
				auth.WriteError(w, http.StatusBadRequest, "same_email",
					"New email is the same as current email")
				return
			}

			// Update email and mark unverified
			_, err := db.ExecContext(r.Context(), `
				UPDATE subscribers SET email = $1, email_verified = false, status = 'pending'
				WHERE id = $2
			`, newEmail, subscriberID)
			if err != nil {
				if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
					auth.WriteError(w, http.StatusConflict, "email_taken",
						"This email address is already in use")
					return
				}
				auth.WriteError(w, http.StatusInternalServerError, "server_error", "Update failed")
				return
			}

			// Invalidate existing verification tokens and generate new one
			db.ExecContext(r.Context(), `
				UPDATE email_verification_tokens SET used_at = now()
				WHERE subscriber_id = $1 AND used_at IS NULL
			`, subscriberID)

			rawToken, err := generateVerificationToken(db, subscriberID.String())
			if err == nil {
				baseURL := getBaseURL()
				verifyURL := fmt.Sprintf("%s/verify?token=%s", baseURL, rawToken)
				go email.SendVerificationEmail(newEmail, displayName, verifyURL)
			}
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Profile updated successfully.",
		})
	}))
}

// HandleDeleteAccount processes DELETE /auth/account.
// Soft-deletes by setting status to 'cancelled' and revoking all tokens.
func HandleDeleteAccount(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Account deletion failed")
			return
		}
		defer tx.Rollback()

		// Soft delete — status = 'cancelled', subscriber data retained 30 days
		tx.ExecContext(r.Context(), `UPDATE subscribers SET status = 'cancelled' WHERE id = $1`, subscriberID)

		// Revoke all refresh tokens
		tx.ExecContext(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE subscriber_id = $1 AND revoked_at IS NULL`, subscriberID)

		// Deactivate all API tokens
		tx.ExecContext(r.Context(), `UPDATE api_tokens SET is_active = false WHERE subscriber_id = $1`, subscriberID)

		// Deactivate all devices
		tx.ExecContext(r.Context(), `UPDATE subscriber_devices SET is_active = false WHERE subscriber_id = $1`, subscriberID)

		// Log account deletion to audit trail
		tx.ExecContext(r.Context(), `
			INSERT INTO audit_log (subscriber_id, action, metadata)
			VALUES ($1, 'account_deleted', '{"method":"self_service"}')
		`, subscriberID)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Account deletion failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Account cancelled. Your data will be retained for 30 days before permanent deletion.",
		})
	}))
}
