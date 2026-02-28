// skip_api.go — Commercial skip API handler.
//
// Provides the GET /stream/{channel_id}/skip-markers?position={seconds} endpoint
// which returns the next commercial marker after the given playback position.
// Owl clients use this to implement one-tap commercial skip.
//
// Data flow:
//  1. Commercial detection events are stored in the commercial_events table.
//  2. This handler queries for events after the given position.
//  3. Returns the earliest upcoming marker or null if none found.
//
// The endpoint is integrated into the owl_api service via the SkipHandler type.
package commercials

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SkipMarker is a single upcoming commercial marker returned by the API.
type SkipMarker struct {
	EventID  string  `json:"event_id"`
	StartSec float64 `json:"start_sec"`
	EndSec   float64 `json:"end_sec"`
}

// SkipResponse is the JSON response from the skip-markers endpoint.
type SkipResponse struct {
	ChannelID  string      `json:"channel_id"`
	Position   float64     `json:"position"`
	NextSkip   *SkipMarker `json:"next_skip"` // null when no upcoming marker
}

// SkipDB is the minimal DB interface for skip queries.
type SkipDB interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// SkipHandler handles the /stream/{channel_id}/skip-markers endpoint.
type SkipHandler struct {
	db                SkipDB
	minConfidence     float64 // minimum confidence to include a marker (default 0.75)
}

// NewSkipHandler creates a SkipHandler.
func NewSkipHandler(db SkipDB, minConfidence float64) *SkipHandler {
	if minConfidence <= 0 {
		minConfidence = 0.75
	}
	return &SkipHandler{db: db, minConfidence: minConfidence}
}

// ServeHTTP handles GET /stream/{channel_id}/skip-markers?position={seconds}
//
// Path format expected: /stream/{channel_id}/skip-markers
// The channel_id is extracted from the path segment between /stream/ and /skip-markers.
func (h *SkipHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSkipError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	channelID := extractChannelID(r.URL.Path)
	if channelID == "" {
		writeSkipError(w, http.StatusBadRequest, "missing_channel_id")
		return
	}

	positionStr := r.URL.Query().Get("position")
	if positionStr == "" {
		writeSkipError(w, http.StatusBadRequest, "missing_position")
		return
	}
	position, err := strconv.ParseFloat(positionStr, 64)
	if err != nil || position < 0 {
		writeSkipError(w, http.StatusBadRequest, "invalid_position")
		return
	}

	marker, err := h.nextMarker(r.Context(), channelID, position)
	if err != nil {
		writeSkipError(w, http.StatusInternalServerError, "db_error")
		return
	}

	resp := SkipResponse{
		ChannelID: channelID,
		Position:  position,
		NextSkip:  marker,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// nextMarker queries for the next commercial event after the given position.
// Position is measured in seconds from the epoch start of started_at.
// We convert to a timestamp by treating position as seconds offset from
// the channel's current stream window start.
//
// For simplicity in the reference implementation we query events by
// started_at > (now - grace_period) ordered by started_at, which covers
// time-shifted viewing with a reasonable DVR window.
func (h *SkipHandler) nextMarker(ctx context.Context, channelID string, positionSec float64) (*SkipMarker, error) {
	// Calculate the absolute timestamp for the position.
	// For live time-shifted viewing, position is seconds behind live edge.
	// We find commercial events that started after (now - position) seconds ago.
	targetTime := time.Now().Add(-time.Duration(positionSec) * time.Second)

	row := h.db.QueryRowContext(ctx, `
		SELECT id,
		       EXTRACT(EPOCH FROM started_at)::float8,
		       EXTRACT(EPOCH FROM COALESCE(ended_at, started_at + INTERVAL '120 seconds'))::float8
		FROM commercial_events
		WHERE channel_id = $1
		  AND status != 'false_positive'
		  AND confidence >= $2
		  AND started_at > $3
		ORDER BY started_at ASC
		LIMIT 1
	`, channelID, h.minConfidence, targetTime)

	var eventID string
	var startEpoch, endEpoch float64
	err := row.Scan(&eventID, &startEpoch, &endEpoch)
	if err == sql.ErrNoRows {
		return nil, nil // no upcoming marker
	}
	if err != nil {
		return nil, fmt.Errorf("nextMarker query: %w", err)
	}

	return &SkipMarker{
		EventID:  eventID,
		StartSec: startEpoch,
		EndSec:   endEpoch,
	}, nil
}

// ---------- helpers ----------------------------------------------------------

// extractChannelID extracts the channel ID from paths like:
//
//	/stream/{channel_id}/skip-markers
//	/owl/v1/stream/{channel_id}/skip-markers
func extractChannelID(path string) string {
	// Normalize: remove leading slash, split on "/"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if (p == "stream" || p == "channels") && i+2 < len(parts) {
			if parts[i+2] == "skip-markers" || parts[i+2] == "commercial" {
				return parts[i+1]
			}
		}
		if p == "stream" && i+1 < len(parts) {
			next := parts[i+1]
			if next != "skip-markers" && next != "" {
				return next
			}
		}
	}
	return ""
}

func writeSkipError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}
