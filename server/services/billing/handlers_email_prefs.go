// handlers_email_prefs.go — Email preference management for P17-T05.
//
// GET  /email/preferences   — get subscriber's email category preferences
// POST /email/preferences   — update one or more category preferences
// GET  /email/unsubscribe   — one-click unsubscribe (token in ?token= query param)
//
// Email categories:
//   billing     — invoices, payment failures, subscription changes (cannot opt out)
//   marketing   — product news, promotions (opt-in by default)
//   product     — feature announcements, tips (opt-in by default)
//   sports      — game reminders, score alerts (opt-in when sports preferences set)
//   trials      — trial lifecycle (cannot opt out while in trial)
//
// The ?token= JWT for unsubscribe is signed with EMAIL_UNSUBSCRIBE_SECRET and
// contains subscriber_id + category. 90-day expiry.
package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
)

// emailCategories defines all valid preference categories and whether they are opt-out-able.
var emailCategories = map[string]bool{
	"billing":   false, // mandatory — cannot opt out
	"trials":    false, // mandatory during trial
	"marketing": true,  // optional
	"product":   true,  // optional
	"sports":    true,  // optional
}

// handleEmailPreferences handles GET and POST /email/preferences.
func (s *Server) handleEmailPreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getEmailPreferences(w, r)
	case http.MethodPost:
		s.updateEmailPreferences(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST only")
	}
}

// getEmailPreferences returns the subscriber's per-category email opt-in status.
func (s *Server) getEmailPreferences(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT category, is_opted_in, updated_at
		FROM email_preferences
		WHERE subscriber_id = $1
	`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "preference lookup failed")
		return
	}
	defer rows.Close()

	prefs := make(map[string]interface{})
	// Start with defaults.
	for cat, optional := range emailCategories {
		prefs[cat] = map[string]interface{}{
			"enabled":  true,
			"optional": optional,
		}
	}
	for rows.Next() {
		var cat string
		var optedIn bool
		var updatedAt time.Time
		if err := rows.Scan(&cat, &optedIn, &updatedAt); err != nil {
			continue
		}
		optional := emailCategories[cat]
		prefs[cat] = map[string]interface{}{
			"enabled":    optedIn,
			"optional":   optional,
			"updated_at": updatedAt.Format(time.RFC3339),
		}
	}

	// Build unsubscribe tokens for optional categories.
	for cat, optional := range emailCategories {
		if !optional {
			continue
		}
		if p, ok := prefs[cat].(map[string]interface{}); ok {
			p["unsubscribe_token"] = buildUnsubscribeToken(subscriberID, cat)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"preferences": prefs})
}

// updateEmailPreferences sets opt-in status for one or more categories.
func (s *Server) updateEmailPreferences(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var req map[string]bool // category → enabled
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "JSON map required")
		return
	}

	for cat, enabled := range req {
		optional, known := emailCategories[cat]
		if !known {
			continue // ignore unknown categories
		}
		if !optional && !enabled {
			continue // cannot opt out of mandatory categories
		}
		_, _ = s.db.ExecContext(r.Context(), `
			INSERT INTO email_preferences (subscriber_id, category, is_opted_in)
			VALUES ($1, $2, $3)
			ON CONFLICT (subscriber_id, category) DO UPDATE
			SET is_opted_in = EXCLUDED.is_opted_in, updated_at = now()
		`, subscriberID, cat, enabled)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleEmailUnsubscribe handles GET /email/unsubscribe?token=JWT&category=X.
// One-click unsubscribe per RFC 8058. Token validates the subscriber and category.
func (s *Server) handleEmailUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	category := r.URL.Query().Get("category")
	if token == "" || category == "" {
		http.Error(w, "Missing token or category", http.StatusBadRequest)
		return
	}

	subscriberID, err := verifyUnsubscribeToken(token, category)
	if err != nil {
		http.Error(w, "Invalid or expired unsubscribe link", http.StatusBadRequest)
		return
	}

	// Check category is opt-out-able.
	optional, known := emailCategories[category]
	if !known || !optional {
		http.Error(w, "This email category cannot be unsubscribed", http.StatusBadRequest)
		return
	}

	_, _ = s.db.ExecContext(r.Context(), `
		INSERT INTO email_preferences (subscriber_id, category, is_opted_in)
		VALUES ($1, $2, FALSE)
		ON CONFLICT (subscriber_id, category) DO UPDATE
		SET is_opted_in = FALSE, updated_at = now()
	`, subscriberID, category)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	baseURL := getEnv("ROOST_BASE_URL", "https://roost.unity.dev")
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Unsubscribed</title>
<style>body{font-family:sans-serif;max-width:480px;margin:80px auto;padding:0 20px;color:#222}</style>
</head>
<body>
<h2>You've been unsubscribed</h2>
<p>You will no longer receive <strong>%s</strong> emails from Roost.</p>
<p><a href="%s/dashboard/email-preferences">Manage all email preferences</a></p>
</body></html>`, category, baseURL)
}

// buildUnsubscribeToken creates an HMAC-signed token: base64(subscriberID:category:expires:sig).
func buildUnsubscribeToken(subscriberID, category string) string {
	expires := time.Now().Add(90 * 24 * time.Hour).Unix()
	payload := fmt.Sprintf("%s:%s:%d", subscriberID, category, expires)
	sig := emailHMAC(payload)
	return base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%s", payload, sig)))
}

// verifyUnsubscribeToken validates the token and returns subscriberID.
func verifyUnsubscribeToken(token, expectedCategory string) (string, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("invalid token encoding")
	}
	parts := strings.SplitN(string(raw), ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed token")
	}
	payload, sig := parts[0], parts[1]
	if emailHMAC(payload) != sig {
		return "", fmt.Errorf("invalid signature")
	}

	// Parse payload: subscriberID:category:expires
	fields := strings.SplitN(payload, ":", 3)
	if len(fields) != 3 {
		return "", fmt.Errorf("malformed payload")
	}
	subscriberID, category := fields[0], fields[1]
	var expiresUnix int64
	fmt.Sscanf(fields[2], "%d", &expiresUnix)
	if time.Now().Unix() > expiresUnix {
		return "", fmt.Errorf("token expired")
	}
	if category != expectedCategory {
		return "", fmt.Errorf("category mismatch")
	}
	return subscriberID, nil
}

// emailHMAC computes HMAC-SHA256 of payload using EMAIL_UNSUBSCRIBE_SECRET.
func emailHMAC(payload string) string {
	secret := os.Getenv("EMAIL_UNSUBSCRIBE_SECRET")
	if secret == "" {
		secret = "roost-email-unsub-secret-change-in-prod"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
