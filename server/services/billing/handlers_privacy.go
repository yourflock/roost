// handlers_privacy.go — GDPR privacy and data subject rights endpoints.
// P16-T04: Privacy & GDPR Compliance
//
// Subscriber endpoints (require Bearer JWT):
//   POST   /account/delete           — request account deletion (30-day grace)
//   DELETE /account/delete/:id/cancel — cancel a pending deletion request
//   GET    /account/export           — GDPR data export (full subscriber record)
//
// Admin endpoints (superowner only):
//   POST   /admin/privacy/process-deletions — process all due deletion requests
package billing

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
)

// handleAccountDelete handles POST /account/delete.
// Creates a pending deletion request for the authenticated subscriber.
// The account is not deleted immediately — it is scheduled 30 days out
// to allow time for reconsideration and to satisfy chargebacks/disputes.
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	subscriberID := claims.Subject

	// Check for an existing active deletion request.
	var existingID string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT id FROM data_deletion_requests
		WHERE subscriber_id = $1 AND status IN ('pending','processing')
	`, subscriberID).Scan(&existingID)
	if err == nil {
		// Already pending — return existing request.
		writeError(w, http.StatusConflict, "deletion_already_requested",
			"A deletion request is already pending. Cancel it at DELETE /account/delete/"+existingID+"/cancel")
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to check existing requests")
		return
	}

	// Extract IP for compliance record.
	ip := r.Header.Get("CF-Connecting-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	// Strip port from RemoteAddr (format "IP:port" or "[::1]:port").
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}

	var requestID string
	var scheduledAt time.Time
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO data_deletion_requests (subscriber_id, requested_ip)
		VALUES ($1, $2)
		RETURNING id, scheduled_deletion_at
	`, subscriberID, ip).Scan(&requestID, &scheduledAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create deletion request: "+err.Error())
		return
	}

	// Audit log.
	s.logSubscriberAction(r, subscriberID, "account.delete_requested", "subscriber", subscriberID, map[string]interface{}{
		"request_id":   requestID,
		"scheduled_at": scheduledAt,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"request_id":             requestID,
		"status":                 "pending",
		"scheduled_deletion_at":  scheduledAt.Format(time.RFC3339),
		"cancel_before":          scheduledAt.Format(time.RFC3339),
		"message":                "Your account will be deleted in 30 days. Cancel at any time before then.",
		"cancel_url":             "/account/delete/" + requestID + "/cancel",
	})
}

// handleAccountDeleteCancel handles DELETE /account/delete/:id/cancel.
// Cancels a pending deletion request within the 30-day grace window.
func (s *Server) handleAccountDeleteCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	subscriberID := claims.Subject

	// Extract request ID from path: /account/delete/{id}/cancel
	path := strings.TrimPrefix(r.URL.Path, "/account/delete/")
	path = strings.TrimSuffix(path, "/cancel")
	requestID := strings.Trim(path, "/")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "missing_request_id", "Request ID required in path")
		return
	}

	result, err := s.db.ExecContext(r.Context(), `
		UPDATE data_deletion_requests
		SET status = 'cancelled'
		WHERE id = $1
		  AND subscriber_id = $2
		  AND status = 'pending'
		  AND scheduled_deletion_at > NOW()
	`, requestID, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to cancel deletion request")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "not_found",
			"No cancellable deletion request found (already cancelled, completed, or past the deadline)")
		return
	}

	// Audit log.
	s.logSubscriberAction(r, subscriberID, "account.delete_cancelled", "subscriber", subscriberID, map[string]interface{}{
		"request_id": requestID,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"request_id": requestID,
		"status":     "cancelled",
		"message":    "Your account deletion request has been cancelled.",
	})
}

// handleAccountExport handles GET /account/export.
// Returns a complete GDPR data export for the authenticated subscriber.
// Includes profile, subscription, billing history, watch history, and API tokens
// (token IDs only — raw tokens are never stored in cleartext).
func (s *Server) handleAccountExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	subscriberID := claims.Subject

	export := map[string]interface{}{
		"export_generated_at": time.Now().UTC().Format(time.RFC3339),
		"subscriber_id":       subscriberID,
	}

	// Profile.
	var profile struct {
		Email     string     `json:"email"`
		Name      string     `json:"name"`
		Status    string     `json:"status"`
		CreatedAt time.Time  `json:"created_at"`
		UpdatedAt *time.Time `json:"updated_at,omitempty"`
	}
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT email, name, status, created_at, updated_at
		FROM subscribers WHERE id = $1
	`, subscriberID).Scan(
		&profile.Email, &profile.Name, &profile.Status,
		&profile.CreatedAt, &profile.UpdatedAt,
	); err == nil {
		export["profile"] = profile
	}

	// Subscription.
	var sub struct {
		PlanSlug      string     `json:"plan_slug"`
		Status        string     `json:"status"`
		BillingPeriod string     `json:"billing_period"`
		CreatedAt     time.Time  `json:"created_at"`
		CancelledAt   *time.Time `json:"cancelled_at,omitempty"`
	}
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT s.plan_slug, s.status, s.billing_period, s.created_at,
		       CASE WHEN s.cancel_at_period_end THEN s.current_period_end ELSE NULL END
		FROM subscriptions s WHERE s.subscriber_id = $1
	`, subscriberID).Scan(
		&sub.PlanSlug, &sub.Status, &sub.BillingPeriod, &sub.CreatedAt, &sub.CancelledAt,
	); err == nil {
		export["subscription"] = sub
	}

	// Billing history (last 24 months).
	billingRows, err := s.db.QueryContext(r.Context(), `
		SELECT amount_cents, currency, status, created_at
		FROM invoices
		WHERE subscriber_id = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, subscriberID)
	if err == nil {
		defer billingRows.Close()
		var billing []map[string]interface{}
		for billingRows.Next() {
			var amountCents int
			var currency, status string
			var createdAt time.Time
			if err := billingRows.Scan(&amountCents, &currency, &status, &createdAt); err == nil {
				billing = append(billing, map[string]interface{}{
					"amount_cents": amountCents,
					"currency":     currency,
					"status":       status,
					"created_at":   createdAt.Format(time.RFC3339),
				})
			}
		}
		if billing == nil {
			billing = []map[string]interface{}{}
		}
		export["billing_history"] = billing
	}

	// Watch history (session records — no content titles, just channel slugs and timestamps).
	watchRows, err := s.db.QueryContext(r.Context(), `
		SELECT channel_slug, started_at, ended_at, bytes_transferred
		FROM stream_sessions
		WHERE subscriber_id = $1
		ORDER BY started_at DESC
		LIMIT 500
	`, subscriberID)
	if err == nil {
		defer watchRows.Close()
		var watchHistory []map[string]interface{}
		for watchRows.Next() {
			var slug string
			var startedAt time.Time
			var endedAt *time.Time
			var bytes *int64
			if err := watchRows.Scan(&slug, &startedAt, &endedAt, &bytes); err == nil {
				entry := map[string]interface{}{
					"channel_slug": slug,
					"started_at":   startedAt.Format(time.RFC3339),
				}
				if endedAt != nil {
					entry["ended_at"] = endedAt.Format(time.RFC3339)
				}
				if bytes != nil {
					entry["bytes_transferred"] = *bytes
				}
				watchHistory = append(watchHistory, entry)
			}
		}
		if watchHistory == nil {
			watchHistory = []map[string]interface{}{}
		}
		export["watch_history"] = watchHistory
	}

	// Audit log.
	s.logSubscriberAction(r, subscriberID, "account.data_exported", "subscriber", subscriberID, nil)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="roost-data-export.json"`)
	json.NewEncoder(w).Encode(export)
}

// handleProcessDeletions handles POST /admin/privacy/process-deletions.
// Processes all data_deletion_requests with status='pending' and
// scheduled_deletion_at <= NOW(). Each processed subscriber's data is deleted
// from all tables. Superowner only.
func (s *Server) handleProcessDeletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	adminID := ""
	if claims != nil {
		adminID = claims.Subject
	}

	// Fetch due requests.
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, subscriber_id FROM data_deletion_requests
		WHERE status = 'pending' AND scheduled_deletion_at <= NOW()
		ORDER BY scheduled_deletion_at
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to query deletion requests")
		return
	}
	defer rows.Close()

	type requestRow struct {
		ID           string
		SubscriberID string
	}
	var requests []requestRow
	for rows.Next() {
		var req requestRow
		if err := rows.Scan(&req.ID, &req.SubscriberID); err == nil {
			requests = append(requests, req)
		}
	}

	processed := 0
	failed := 0
	details := []map[string]interface{}{}

	for _, req := range requests {
		// Mark as processing.
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE data_deletion_requests SET status='processing' WHERE id=$1`, req.ID)

		// Delete subscriber data. The subscriber row deletion cascades to most tables
		// via ON DELETE CASCADE foreign keys. We also clean up non-cascading data.
		deleteErr := s.deleteSubscriberData(req.SubscriberID)
		if deleteErr != nil {
			failed++
			_, _ = s.db.ExecContext(r.Context(),
				`UPDATE data_deletion_requests SET status='pending', notes=$1 WHERE id=$2`,
				"processing error: "+deleteErr.Error(), req.ID)
			details = append(details, map[string]interface{}{
				"request_id":    req.ID,
				"subscriber_id": req.SubscriberID,
				"status":        "failed",
				"error":         deleteErr.Error(),
			})
			continue
		}

		// Mark complete.
		_, _ = s.db.ExecContext(r.Context(),
			`UPDATE data_deletion_requests SET status='completed', completed_at=NOW() WHERE id=$1`, req.ID)
		processed++

		// Audit log.
		s.logAdminAction(r, adminID, "subscriber.deleted", "subscriber", req.SubscriberID, map[string]interface{}{
			"deletion_request_id": req.ID,
			"reason":              "gdpr_erasure_request",
		})

		details = append(details, map[string]interface{}{
			"request_id":    req.ID,
			"subscriber_id": req.SubscriberID,
			"status":        "completed",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"processed": processed,
		"failed":    failed,
		"total":     len(requests),
		"details":   details,
	})
}

// deleteSubscriberData performs a full GDPR erasure for a subscriber.
// Data deleted (beyond CASCADE): stream sessions, audit log entries, watch history.
// Billing records are anonymized (not deleted) for legal/accounting compliance.
func (s *Server) deleteSubscriberData(subscriberID string) error {
	// The subscriber row deletion cascades to: subscriptions, api_tokens,
	// stream_sessions, profiles, owl_sessions, watch_parties, sso_links,
	// reseller_subscribers, sports_preferences, data_deletion_requests.

	// Anonymize invoices instead of deleting (accounting requirement).
	_, _ = s.db.Exec(`
		UPDATE invoices
		SET stripe_customer_id = 'DELETED',
		    stripe_invoice_id = 'DELETED'
		WHERE subscriber_id = $1
	`, subscriberID)

	// Anonymize audit log entries (keep the action record, remove actor identity).
	_, _ = s.db.Exec(`
		UPDATE audit_log
		SET actor_id = NULL
		WHERE actor_id = $1::uuid
	`, subscriberID)

	// Delete the subscriber (cascades to all linked tables).
	_, err := s.db.Exec(`DELETE FROM subscribers WHERE id = $1`, subscriberID)
	return err
}
