// handlers_abuse.go — Abuse detection admin endpoints.
// P16-T05: Abuse Detection
//
// GET  /admin/abuse          — list flagged subscribers (unreviewed first)
// POST /admin/abuse/:id/review — review and resolve a flag
//
// Both endpoints require superowner authorization.
package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// handleAbuseList handles GET /admin/abuse.
// Returns flagged subscribers, unreviewed first, with subscriber email and
// flag details for admin investigation.
func (s *Server) handleAbuseList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT
			f.id, f.subscriber_id, f.flag_reason,
			f.flag_details, f.auto_flagged, f.flagged_at,
			f.reviewed_at, f.resolution,
			sub.email, sub.name
		FROM flagged_subscribers f
		JOIN subscribers sub ON sub.id = f.subscriber_id
		ORDER BY (f.reviewed_at IS NULL) DESC, f.flagged_at DESC
		LIMIT 200
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to query flagged subscribers")
		return
	}
	defer rows.Close()

	type flagEntry struct {
		ID             string                 `json:"id"`
		SubscriberID   string                 `json:"subscriber_id"`
		SubscriberEmail string                `json:"subscriber_email"`
		SubscriberName  string                `json:"subscriber_name"`
		FlagReason     string                 `json:"flag_reason"`
		FlagDetails    map[string]interface{} `json:"flag_details"`
		AutoFlagged    bool                   `json:"auto_flagged"`
		FlaggedAt      string                 `json:"flagged_at"`
		ReviewedAt     *string                `json:"reviewed_at,omitempty"`
		Resolution     *string                `json:"resolution,omitempty"`
	}

	var flags []flagEntry
	for rows.Next() {
		var f flagEntry
		var detailsJSON string
		var reviewedAt *time.Time
		if err := rows.Scan(
			&f.ID, &f.SubscriberID, &f.FlagReason,
			&detailsJSON, &f.AutoFlagged, &f.FlaggedAt,
			&reviewedAt, &f.Resolution,
			&f.SubscriberEmail, &f.SubscriberName,
		); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(detailsJSON), &f.FlagDetails)
		if reviewedAt != nil {
			s := reviewedAt.Format(time.RFC3339)
			f.ReviewedAt = &s
		}
		flags = append(flags, f)
	}
	if flags == nil {
		flags = []flagEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"flags": flags,
		"total": len(flags),
	})
}

// handleAbuseReview handles POST /admin/abuse/:id/review.
// Accepts body: { "resolution": "cleared|suspended|banned", "notes": "..." }
func (s *Server) handleAbuseReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	// Extract flag ID from path: /admin/abuse/{id}/review
	path := strings.TrimPrefix(r.URL.Path, "/admin/abuse/")
	path = strings.TrimSuffix(path, "/review")
	flagID := strings.Trim(path, "/")
	if flagID == "" {
		writeError(w, http.StatusBadRequest, "missing_flag_id", "Flag ID required in path")
		return
	}

	var body struct {
		Resolution string `json:"resolution"`
		Notes      string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}

	validResolutions := map[string]bool{"cleared": true, "suspended": true, "banned": true}
	if !validResolutions[body.Resolution] {
		writeError(w, http.StatusBadRequest, "invalid_resolution", "resolution must be: cleared, suspended, or banned")
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	reviewerID := ""
	if claims != nil {
		reviewerID = claims.Subject
	}

	// Fetch flag to get subscriber ID for subsequent actions.
	var subscriberID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT subscriber_id FROM flagged_subscribers WHERE id = $1`, flagID,
	).Scan(&subscriberID)
	if err != nil {
		writeError(w, http.StatusNotFound, "flag_not_found", "Flag not found")
		return
	}

	// Update flag.
	_, err = s.db.ExecContext(r.Context(), `
		UPDATE flagged_subscribers
		SET resolution = $1, reviewed_at = NOW(), reviewed_by = $2
		WHERE id = $3
	`, body.Resolution, nullIfEmpty(reviewerID), flagID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update flag")
		return
	}

	// Apply resolution consequences.
	switch body.Resolution {
	case "suspended":
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE subscribers SET status = 'suspended' WHERE id = $1`, subscriberID)
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE subscriptions SET status = 'suspended' WHERE subscriber_id = $1`, subscriberID)
		// Revoke all active API tokens.
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE api_tokens SET revoked_at = NOW() WHERE subscriber_id = $1 AND revoked_at IS NULL`, subscriberID)
	case "banned":
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE subscribers SET status = 'banned' WHERE id = $1`, subscriberID)
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE subscriptions SET status = 'cancelled', cancel_at_period_end = false WHERE subscriber_id = $1`, subscriberID)
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE api_tokens SET revoked_at = NOW() WHERE subscriber_id = $1 AND revoked_at IS NULL`, subscriberID)
	}

	// Audit log.
	s.logAdminAction(r, reviewerID, "abuse.reviewed", "subscriber", subscriberID, map[string]interface{}{
		"flag_id":    flagID,
		"resolution": body.Resolution,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"flag_id":    flagID,
		"resolution": body.Resolution,
		"message":    "Flag reviewed. Subscriber status updated.",
	})
}

// nullIfEmpty returns nil for empty strings (used with nullable UUID columns).
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
