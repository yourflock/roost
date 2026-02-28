// handlers_webhooks.go — Parental settings webhook receiver.
// P13-T04: Parental Controls Bridge
//
// External service calls POST /webhooks/parental-settings when a parent updates
// child screen time or content restrictions.
// Roost verifies the HMAC-SHA256 signature and applies the settings to the
// matching subscriber profile.
//
// Also provides PushParentalSettings() — called when Roost parental settings
// change — to keep both systems in sync.
//
// Env vars:
//   PARENTAL_WEBHOOK_SECRET  — shared HMAC secret (required for signature verification)
//   SSO_BASE_URL  — SSO API base (shared with auth handlers)
//   SSO_SERVICE_TOKEN   — bearer token for Roost→SSO API calls
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── Parental settings webhook ─────────────────────────────────────────────────────

// parentalSettings is the payload received on parental setting changes.
type parentalSettings struct {
	SSOUserID      string             `json:"sso_user_id"`
	ChildSSOUserID string             `json:"child_sso_user_id"`
	Settings         childSettings `json:"settings"`
}

type childSettings struct {
	AgeRatingLimit    string   `json:"age_rating_limit"`
	BlockedCategories []string `json:"blocked_categories"`
	ViewingSchedule   *struct {
		AllowedHours struct {
			Start string `json:"start"` // "08:00"
			End   string `json:"end"`   // "21:00"
		} `json:"allowed_hours"`
		Timezone string `json:"timezone"`
	} `json:"viewing_schedule,omitempty"`
}

// handleParentalWebhook receives parental setting change events.
// POST /webhooks/parental-settings
func (s *Server) handleParentalWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	const maxBodyBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		return
	}

	// Verify HMAC-SHA256 signature
	webhookSecret := os.Getenv("PARENTAL_WEBHOOK_SECRET")
	if webhookSecret == "" {
		log.Printf("WARNING: PARENTAL_WEBHOOK_SECRET not set — skipping signature verification (dev only)")
	} else {
		sig := r.Header.Get("X-Signature")
		if sig == "" {
			writeError(w, http.StatusUnauthorized, "missing_signature",
				"X-Signature header required")
			return
		}
		if !verifyHMAC(body, sig, webhookSecret) {
			writeError(w, http.StatusUnauthorized, "invalid_signature",
				"webhook signature verification failed")
			return
		}
	}

	var payload parentalSettings
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if payload.ChildSSOUserID == "" {
		writeError(w, http.StatusBadRequest, "missing_child_user_id",
			"child_sso_user_id is required")
		return
	}

	// Find the kids profile linked to the child
	var profileID string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT sp.id
		FROM subscriber_profiles sp
		JOIN subscribers sub ON sub.id = sp.subscriber_id
		WHERE sub.sso_user_id = $1
		  AND sp.is_kids_profile = true
		ORDER BY sp.created_at ASC
		LIMIT 1
	`, payload.ChildSSOUserID).Scan(&profileID)
	if err == sql.ErrNoRows {
		log.Printf("[parental_webhook] no kids profile for child_sso_user_id=%s", payload.ChildSSOUserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"no_profile_found"}`)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "profile lookup failed")
		return
	}

	// Apply settings to the profile
	blockedCatsJSON, _ := json.Marshal(payload.Settings.BlockedCategories)

	if payload.Settings.ViewingSchedule != nil {
		scheduleJSON, _ := json.Marshal(payload.Settings.ViewingSchedule)
		_, err = s.db.ExecContext(r.Context(), `
			UPDATE subscriber_profiles
			SET age_rating_limit = $1,
			    blocked_categories = $2::jsonb,
			    viewing_schedule = $3::jsonb,
			    updated_at = NOW()
			WHERE id = $4
		`, normaliseAgeRating(payload.Settings.AgeRatingLimit),
			string(blockedCatsJSON), string(scheduleJSON), profileID)
	} else {
		_, err = s.db.ExecContext(r.Context(), `
			UPDATE subscriber_profiles
			SET age_rating_limit = $1,
			    blocked_categories = $2::jsonb,
			    updated_at = NOW()
			WHERE id = $3
		`, normaliseAgeRating(payload.Settings.AgeRatingLimit),
			string(blockedCatsJSON), profileID)
	}
	if err != nil {
		log.Printf("[parental_webhook] settings update failed for profile=%s: %v", profileID, err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to apply parental settings")
		return
	}

	log.Printf("[parental_webhook] parental-settings applied: profile=%s age_limit=%s",
		profileID, payload.Settings.AgeRatingLimit)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","profile_id":%q}`, profileID)
}

// verifyHMAC verifies HMAC-SHA256 of body against the provided signature.
// Provider sends signatures as "sha256=<hex>" or just "<hex>".
func verifyHMAC(body []byte, sig, secret string) bool {
	sig = strings.TrimPrefix(sig, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

// normaliseAgeRating maps MPAA/TV rating strings to Roost-standard values.
func normaliseAgeRating(rating string) string {
	switch strings.ToUpper(strings.TrimSpace(rating)) {
	case "G", "TV-G", "TV-Y", "TV-Y7":
		return "G"
	case "PG", "TV-PG":
		return "PG"
	case "PG-13", "TV-14":
		return "PG-13"
	case "R", "TV-MA":
		return "R"
	case "NC-17", "X":
		return "NC-17"
	default:
		if rating != "" {
			return rating
		}
		return "PG" // safe default for kids profiles
	}
}

// ── Push parental settings ────────────────────────────────────────────────────

// PushParentalSettings pushes a Roost subscriber's profile parental settings to
// the SSO provider API when settings are changed in Roost.
// Runs in a goroutine — does not block the calling request.
// No-ops if the subscriber is not linked to SSO.
func (s *Server) PushParentalSettings(ctx context.Context, subscriberID, profileID string) {
	// Fetch subscriber's sso_user_id
	var ssoUserID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT sso_user_id FROM subscribers WHERE id = $1`, subscriberID).Scan(&ssoUserID)
	if err != nil || !ssoUserID.Valid {
		return // not linked to SSO
	}

	// Fetch profile settings
	var ageRating sql.NullString
	var blockedCatsJSON sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT age_rating_limit, blocked_categories::text
		FROM subscriber_profiles WHERE id = $1`, profileID).Scan(&ageRating, &blockedCatsJSON)
	if err != nil {
		log.Printf("[parental_push] read profile %s: %v", profileID, err)
		return
	}

	var blocked []string
	if blockedCatsJSON.Valid && blockedCatsJSON.String != "" {
		_ = json.Unmarshal([]byte(blockedCatsJSON.String), &blocked)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"sso_user_id": ssoUserID.String,
		"settings": map[string]interface{}{
			"age_rating_limit":   ageRating.String,
			"blocked_categories": blocked,
			"source":             "roost",
		},
	})

	pushURL := fmt.Sprintf("%s/api/parental/external-settings", ssoBaseURL())
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, pushURL,
			bytes.NewReader(payload))
		if err != nil {
			log.Printf("[parental_push] build request error: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if tok := os.Getenv("SSO_SERVICE_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[parental_push] SSO API unreachable: %v", err)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 400 {
			log.Printf("[parental_push] non-OK status %d pushing parental settings", resp.StatusCode)
		}
	}()
}
