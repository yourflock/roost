// Package handlers — admin audit log query handler.
//
// GET /admin/audit returns the most recent admin write actions for the caller's
// Roost server. The response is ordered by occurred_at DESC (newest first).
// Rows are read from admin_audit_log which is insert-only — never UPDATE/DELETE.
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/yourflock/roost/services/owl_api/middleware"
)

// AuditLogRow is one row returned by GET /admin/audit.
type AuditLogRow struct {
	ID          string                 `json:"id"`
	FlockUserID string                 `json:"flock_user_id"`
	Action      string                 `json:"action"`
	TargetID    *string                `json:"target_id,omitempty"`
	Details     map[string]interface{} `json:"details,omitempty"`
	OccurredAt  time.Time              `json:"occurred_at"`
}

// ListAuditLog handles GET /admin/audit.
//
// Query params:
//   - limit: max rows to return (default 100, max 500)
//   - action: filter by action prefix (e.g. "storage.")
//   - since: ISO-8601 timestamp — only return rows after this time
//
// Returns rows ordered newest-first.
func (h *AdminHandlers) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	// Parse query params
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}

	actionFilter := r.URL.Query().Get("action")
	sinceStr := r.URL.Query().Get("since")

	var since time.Time
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	query := `
		SELECT id, flock_user_id, action, target_id, details, occurred_at
		  FROM admin_audit_log
		 WHERE roost_id = $1`
	args := []interface{}{claims.RoostID}
	argN := 2

	if actionFilter != "" {
		query += ` AND action LIKE $` + strconv.Itoa(argN)
		args = append(args, actionFilter+"%")
		argN++
	}
	if !since.IsZero() {
		query += ` AND occurred_at > $` + strconv.Itoa(argN)
		args = append(args, since)
		argN++
	}

	query += ` ORDER BY occurred_at DESC LIMIT $` + strconv.Itoa(argN)
	args = append(args, limit)

	rows, err := h.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []AuditLogRow
	for rows.Next() {
		var e AuditLogRow
		var detailsJSON []byte
		if err := rows.Scan(&e.ID, &e.FlockUserID, &e.Action, &e.TargetID, &detailsJSON, &e.OccurredAt); err != nil {
			continue
		}
		if len(detailsJSON) > 0 {
			e.Details = map[string]interface{}{}
			_ = json.Unmarshal(detailsJSON, &e.Details)
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []AuditLogRow{}
	}
	writeAdminJSON(w, http.StatusOK, entries)
}
