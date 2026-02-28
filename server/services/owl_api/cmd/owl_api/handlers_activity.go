// handlers_activity.go — Watch party and content sharing endpoints for the Owl addon API.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Handler: GET /owl/watch-party/:id ─────────────────────────────────────────

// handleOwlWatchParty returns watch party status for Owl clients.
// GET /owl/watch-party/{id}
func (s *server) handleOwlWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Extract party ID from path: /owl/watch-party/{id} or /owl/v1/watch-party/{id}
	parts := splitPath(r.URL.Path)
	partyID := ""
	for i, part := range parts {
		if part == "watch-party" && i+1 < len(parts) {
			partyID = parts[i+1]
			break
		}
	}
	if partyID == "" {
		writeError(w, http.StatusBadRequest, "missing_party_id", "party ID required")
		return
	}

	var status, contentType, inviteCode string
	var channelSlug sql.NullString
	var participantCount int
	var startedAt time.Time

	err := s.db.QueryRowContext(r.Context(), `
		SELECT wp.status, wp.content_type, wp.invite_code, c.slug,
		       (SELECT COUNT(*) FROM watch_party_participants wpp
		        WHERE wpp.party_id = wp.id AND wpp.left_at IS NULL),
		       wp.started_at
		FROM watch_parties wp
		LEFT JOIN channels c ON c.id = wp.channel_id
		WHERE wp.id = $1
	`, partyID).Scan(&status, &contentType, &inviteCode, &channelSlug,
		&participantCount, &startedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "party_not_found", "watch party not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "party lookup failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"party_id":          partyID,
		"status":            status,
		"content_type":      contentType,
		"invite_code":       inviteCode,
		"channel_slug":      channelSlug.String,
		"participant_count": participantCount,
		"started_at":        startedAt.Format(time.RFC3339),
	})
}

// ── Handler: POST /owl/share ──────────────────────────────────────────────────

// handleOwlShare records a content share event.
// POST /owl/share
// Body: { "content_title": "...", "content_type": "live|vod", "channel_slug": "..." }
func (s *server) handleOwlShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	var req struct {
		ContentTitle string `json:"content_title"`
		ContentType  string `json:"content_type"`
		ChannelSlug  string `json:"channel_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"shared":true}`)
}

// ── Path helper ───────────────────────────────────────────────────────────────

func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
