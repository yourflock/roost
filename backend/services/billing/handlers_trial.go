// handlers_trial.go — Free trial system for P17-T01.
//
// POST /billing/trial  — start a 7-day free trial (no credit card required)
// GET  /billing/trial  — check trial status for authenticated subscriber
//
// Trial limits enforced by token validation middleware:
//   - Max quality: 720p HLS variant
//   - Max concurrent streams: 1
//
// Abuse prevention:
//   - Email hash checked against trial_abuse_tracking
//   - Max 3 trials per IP address per 30 days
package billing

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

const trialDurationDays = 7

// trialStartResponse is returned when a trial is successfully started.
type trialStartResponse struct {
	SubscriptionID string    `json:"subscription_id"`
	TrialStart     time.Time `json:"trial_start"`
	TrialEnd       time.Time `json:"trial_end"`
	APIToken       string    `json:"api_token"`
	Message        string    `json:"message"`
}

// handleTrial routes POST and GET for /billing/trial.
func (s *Server) handleTrial(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.startTrial(w, r)
	case http.MethodGet:
		s.getTrialStatus(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

// startTrial starts a 7-day free trial for an authenticated subscriber.
// POST /billing/trial
func (s *Server) startTrial(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Fetch subscriber email for abuse checking.
	var email string
	if err := s.db.QueryRow(`SELECT email FROM subscribers WHERE id = $1`, subscriberID).Scan(&email); err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "subscriber not found")
		return
	}

	// Check for existing subscription.
	var existingStatus string
	err = s.db.QueryRow(
		`SELECT status FROM subscriptions WHERE subscriber_id = $1 ORDER BY created_at DESC LIMIT 1`,
		subscriberID,
	).Scan(&existingStatus)
	if err == nil {
		// Subscriber already has a subscription.
		switch existingStatus {
		case "trialing":
			auth.WriteError(w, http.StatusConflict, "trial_active", "you already have an active trial")
			return
		case "active":
			auth.WriteError(w, http.StatusConflict, "already_subscribed", "you already have an active subscription")
			return
		}
	}

	// Abuse check: email hash.
	emailHash := hashEmail(email)
	var emailAbuse int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM trial_abuse_tracking WHERE email_hash = $1`, emailHash,
	).Scan(&emailAbuse)
	if emailAbuse > 0 {
		auth.WriteError(w, http.StatusForbidden, "trial_already_used",
			"a trial has already been used with this email address")
		return
	}

	// Abuse check: IP rate limit (max 3 per IP per 30 days).
	clientIP := extractClientIP(r)
	var ipCount int
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM trial_abuse_tracking
		WHERE ip_address = $1::inet AND created_at >= now() - INTERVAL '30 days'
	`, clientIP).Scan(&ipCount)
	if ipCount >= 3 {
		auth.WriteError(w, http.StatusTooManyRequests, "ip_limit_reached",
			"too many trials from this IP address")
		return
	}

	// Generate API token.
	token, err := generateAPIToken()
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "token_error", "failed to generate API token")
		return
	}

	// Look up a default plan for trialling (basic).
	var planID string
	_ = s.db.QueryRow(`SELECT id FROM subscription_plans WHERE slug = 'basic' LIMIT 1`).Scan(&planID)

	// Create the trial subscription.
	now := time.Now().UTC()
	trialEnd := now.AddDate(0, 0, trialDurationDays)
	var subscriptionID string
	err = s.db.QueryRow(`
		INSERT INTO subscriptions
			(subscriber_id, plan_id, status, is_trial, trial_start, trial_end, billing_period)
		VALUES
			($1, NULLIF($2,'')::uuid, 'trialing', TRUE, $3, $4, 'monthly')
		RETURNING id
	`, subscriberID, planID, now, trialEnd).Scan(&subscriptionID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to create trial: "+err.Error())
		return
	}

	// Store API token linked to subscription.
	// token_hash and token_prefix are required NOT NULL columns.
	tokenHash := auth.HashToken(token)
	tokenPrefix := token
	if len(tokenPrefix) > 8 {
		tokenPrefix = tokenPrefix[:8]
	}
	if _, err = s.db.Exec(`
		INSERT INTO api_tokens (subscriber_id, subscription_id, token, token_hash, token_prefix, is_active)
		VALUES ($1, $2, $3, $4, $5, TRUE)
		ON CONFLICT DO NOTHING
	`, subscriberID, subscriptionID, token, tokenHash, tokenPrefix); err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to store API token")
		return
	}

	// Record abuse tracking AFTER successful creation.
	_, _ = s.db.Exec(`
		INSERT INTO trial_abuse_tracking (email_hash, ip_address) VALUES ($1, $2::inet)
	`, emailHash, clientIP)

	// Create onboarding progress row.
	_, _ = s.db.Exec(`
		INSERT INTO onboarding_progress (subscriber_id) VALUES ($1) ON CONFLICT DO NOTHING
	`, subscriberID)

	writeJSON(w, http.StatusCreated, trialStartResponse{
		SubscriptionID: subscriptionID,
		TrialStart:     now,
		TrialEnd:       trialEnd,
		APIToken:       token,
		Message:        fmt.Sprintf("Trial started. Enjoy %d days of Roost. No credit card required.", trialDurationDays),
	})
}

// getTrialStatus returns the current trial state for the authenticated subscriber.
// GET /billing/trial
func (s *Server) getTrialStatus(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}

	var subID string
	var trialStart, trialEnd time.Time
	var convertedAt *time.Time
	err = s.db.QueryRow(`
		SELECT id, trial_start, trial_end, trial_converted_at
		FROM subscriptions
		WHERE subscriber_id = $1 AND is_trial = TRUE
		ORDER BY created_at DESC LIMIT 1
	`, claims.Subject).Scan(&subID, &trialStart, &trialEnd, &convertedAt)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "no_trial", "no trial found for this subscriber")
		return
	}

	remaining := time.Until(trialEnd)
	if remaining < 0 {
		remaining = 0
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"subscription_id": subID,
		"trial_start":     trialStart,
		"trial_end":       trialEnd,
		"days_remaining":  int(remaining.Hours() / 24),
		"converted":       convertedAt != nil,
		"converted_at":    convertedAt,
	})
}

// IsTrialToken checks whether a token belongs to a trialling subscription.
// Returns (isTrial bool, err error). Used by stream relay middleware.
func (s *Server) IsTrialToken(token string) (bool, error) {
	var isTrial bool
	err := s.db.QueryRow(`
		SELECT s.is_trial
		FROM api_tokens t
		JOIN subscriptions s ON s.id = t.subscription_id
		WHERE t.token = $1 AND t.is_active = TRUE
	`, token).Scan(&isTrial)
	return isTrial, err
}

// hashEmail returns a SHA-256 hex hash of the lowercased, trimmed email.
// We never store the raw email in abuse tracking.
func hashEmail(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", h)
}

// generateAPIToken returns a cryptographically random 32-character alphanumeric token.
func generateAPIToken() (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return string(b), nil
}

// extractClientIP extracts the best-effort client IP from request headers.
// Prefer X-Forwarded-For (Cloudflare/Nginx sets this) over RemoteAddr.
func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be a comma-separated list; first is the real client.
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	// Fall back to RemoteAddr (strip port).
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return strings.Trim(host, "[]")
}
