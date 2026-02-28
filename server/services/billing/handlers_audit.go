// handlers_audit.go — Audit log admin endpoints.
// P16-T01: Structured Logging & Audit Trail
//
// GET /admin/audit — paginated, filterable audit log viewer (superowner only)
//
// Query parameters:
//   actor_id      UUID — filter by actor
//   action        string — partial match on action name (e.g. "channel")
//   resource_type string — exact match on resource type
//   resource_id   UUID — filter by resource
//   date_from     RFC3339 — earliest created_at to include
//   date_to       RFC3339 — latest created_at to include
//   page          int — page number (default 1)
//   per_page      int — results per page (default 50, max 200)
package billing

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/unyeco/roost/pkg/audit"
)

// handleAuditLog serves GET /admin/audit.
// Requires superowner authorization (same pattern as all other /admin/ routes).
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	q := r.URL.Query()
	filters := map[string]string{}
	for _, key := range []string{"actor_id", "action", "resource_type", "resource_id", "date_from", "date_to"} {
		if v := q.Get(key); v != "" {
			filters[key] = v
		}
	}

	page := 1
	if p := q.Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	perPage := 50
	if pp := q.Get("per_page"); pp != "" {
		if n, err := strconv.Atoi(pp); err == nil && n > 0 && n <= 200 {
			perPage = n
		}
	}
	offset := (page - 1) * perPage

	entries, total, err := audit.QueryAuditLog(r.Context(), s.db, filters, perPage, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to query audit log")
		return
	}

	totalPages := total / perPage
	if total%perPage != 0 {
		totalPages++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries":     entries,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	})
}
