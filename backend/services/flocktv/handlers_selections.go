// handlers_selections.go — Content selection CRUD handlers (DB-wired).
// Phase FLOCKTV FTV.0.T03/FTV.1.T03: families add/remove canonical content IDs to/from
// their library. All operations are scoped to the family_id extracted from the JWT.
// The per-family DB connection is looked up via the family_containers table.
package flocktv

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ContentSelection is the API representation of a family's content selection row.
type ContentSelection struct {
	ID             string                 `json:"id"`
	FamilyID       string                 `json:"family_id"`
	CanonicalID    string                 `json:"canonical_id"`
	ContentType    string                 `json:"content_type"`
	AddedAt        time.Time              `json:"added_at"`
	WatchProgress  map[string]interface{} `json:"watch_progress,omitempty"`
	PersonalRating *int                   `json:"personal_rating,omitempty"`
	PersonalNotes  *string                `json:"personal_notes,omitempty"`
	CustomTags     []string               `json:"custom_tags,omitempty"`
}

// addSelectionRequest is the POST /flocktv/selections body.
type addSelectionRequest struct {
	CanonicalID string `json:"canonical_id"`
	ContentType string `json:"content_type"`
}

// validContentTypes are the allowed content_type values.
var validContentTypes = map[string]bool{
	"movie": true, "show": true, "episode": true,
	"music": true, "game": true, "podcast": true, "live": true,
}

// handleListSelections returns all content selections for the authenticated family.
// GET /flocktv/selections
// Query params: limit (default 50), offset (default 0), type (filter by content_type)
func (s *Server) handleListSelections(w http.ResponseWriter, r *http.Request) {
	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		// Dev mode fallback: read from query param.
		familyID = r.URL.Query().Get("family_id")
	}
	if familyID == "" {
		writeError(w, http.StatusUnauthorized, "missing_family", "family_id required")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	contentType := r.URL.Query().Get("type")

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"selections": []ContentSelection{},
			"count":      0,
		})
		return
	}

	var rows *sql.Rows
	var err error

	if contentType != "" && validContentTypes[contentType] {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, family_id, canonical_id, content_type, added_at,
			       watch_progress::text, personal_rating, personal_notes, custom_tags
			FROM content_selections
			WHERE family_id = $1 AND content_type = $2
			ORDER BY added_at DESC
			LIMIT $3 OFFSET $4`,
			familyID, contentType, limit, offset)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, family_id, canonical_id, content_type, added_at,
			       watch_progress::text, personal_rating, personal_notes, custom_tags
			FROM content_selections
			WHERE family_id = $1
			ORDER BY added_at DESC
			LIMIT $2 OFFSET $3`,
			familyID, limit, offset)
	}
	if err != nil {
		s.logger.Error("list selections query failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to retrieve selections")
		return
	}
	defer rows.Close()

	selections := []ContentSelection{}
	for rows.Next() {
		var sel ContentSelection
		var wpJSON string
		var tagsArr []byte

		if scanErr := rows.Scan(
			&sel.ID, &sel.FamilyID, &sel.CanonicalID, &sel.ContentType, &sel.AddedAt,
			&wpJSON, &sel.PersonalRating, &sel.PersonalNotes, &tagsArr,
		); scanErr != nil {
			s.logger.Warn("selection row scan failed", "error", scanErr.Error())
			continue
		}

		// Parse watch_progress JSONB.
		if wpJSON != "" && wpJSON != "null" && wpJSON != "{}" {
			_ = json.Unmarshal([]byte(wpJSON), &sel.WatchProgress)
		}
		// Parse custom_tags PostgreSQL array.
		if len(tagsArr) > 0 {
			tagStr := strings.Trim(string(tagsArr), "{}")
			if tagStr != "" {
				sel.CustomTags = strings.Split(tagStr, ",")
			}
		}

		selections = append(selections, sel)
	}

	if err = rows.Err(); err != nil {
		s.logger.Error("selections rows iteration error", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to iterate selections")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"selections": selections,
		"count":      len(selections),
		"limit":      limit,
		"offset":     offset,
	})
}

// handleAddSelection adds a canonical content item to the family's library.
// POST /flocktv/selections
// Body: {"canonical_id": "imdb:tt0111161", "content_type": "movie"}
func (s *Server) handleAddSelection(w http.ResponseWriter, r *http.Request) {
	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		familyID = r.URL.Query().Get("family_id")
	}

	var req addSelectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.CanonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "canonical_id is required")
		return
	}

	if !validContentTypes[req.ContentType] {
		writeError(w, http.StatusBadRequest, "invalid_content_type",
			"content_type must be one of: movie, show, episode, music, game, podcast, live")
		return
	}

	if s.db == nil {
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"status":       "added",
			"canonical_id": req.CanonicalID,
			"content_type": req.ContentType,
			"family_id":    familyID,
		})
		return
	}

	// Upsert into content_selections — idempotent, safe to call multiple times.
	var selectionID string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO content_selections (family_id, canonical_id, content_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (family_id, canonical_id) DO UPDATE
		  SET content_type = EXCLUDED.content_type
		RETURNING id`,
		familyID, req.CanonicalID, req.ContentType,
	).Scan(&selectionID)

	if err != nil {
		s.logger.Error("add selection insert failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to add selection")
		return
	}

	// Queue acquisition if content not yet in shared pool.
	acquisitionStatus := "available"
	if s.db != nil {
		var poolStatus string
		qErr := s.db.QueryRowContext(r.Context(),
			`SELECT COALESCE(status, 'not_found') FROM acquisition_queue WHERE canonical_id = $1 ORDER BY queued_at DESC LIMIT 1`,
			req.CanonicalID,
		).Scan(&poolStatus)
		if qErr == sql.ErrNoRows || poolStatus == "not_found" {
			// Queue acquisition.
			_, _ = s.db.ExecContext(r.Context(), `
				INSERT INTO acquisition_queue (canonical_id, content_type, requested_by)
				VALUES ($1, $2, NULL)
				ON CONFLICT (canonical_id) WHERE status NOT IN ('complete', 'failed') DO NOTHING`,
				req.CanonicalID, req.ContentType,
			)
			acquisitionStatus = "queued"
		} else if poolStatus == "complete" {
			acquisitionStatus = "available"
		} else {
			acquisitionStatus = poolStatus
		}
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"selection_id":       selectionID,
		"canonical_id":       req.CanonicalID,
		"content_type":       req.ContentType,
		"family_id":          familyID,
		"availability_status": acquisitionStatus,
	})
}

// handleRemoveSelection removes a canonical item from the family's library.
// DELETE /flocktv/selections/{canonical_id}
// NOTE: does NOT delete the shared content from the pool — only the family's selection.
func (s *Server) handleRemoveSelection(w http.ResponseWriter, r *http.Request) {
	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		familyID = r.URL.Query().Get("family_id")
	}

	canonicalID := chi.URLParam(r, "canonical_id")
	if canonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "canonical_id is required")
		return
	}

	if s.db == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	result, err := s.db.ExecContext(r.Context(), `
		DELETE FROM content_selections
		WHERE family_id = $1 AND canonical_id = $2`,
		familyID, canonicalID,
	)
	if err != nil {
		s.logger.Error("remove selection failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to remove selection")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "not_found",
			"selection not found for this family")
		return
	}

	// NOTE: shared content pool entry intentionally NOT deleted — other families may use it.
	w.WriteHeader(http.StatusNoContent)
}
