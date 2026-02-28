// source_registry.go — CRUD API for the sports stream source registry.
// OSG.1.002: Register, list, inspect, soft-delete, and refresh stream sources.
package sports

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// ─── POST /sports/sources ─────────────────────────────────────────────────────

// handleCreateSource registers a new stream source.
// Validates M3U URL reachability (HEAD, 5s timeout) before inserting.
func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string  `json:"name"`
		SourceType    string  `json:"source_type"`
		M3UURL        *string `json:"m3u_url"`
		RoostFamilyID *string `json:"roost_family_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "name is required")
		return
	}
	switch req.SourceType {
	case "roost_boost", "manual", "iptv_url":
	default:
		writeError(w, http.StatusBadRequest, "invalid_source_type",
			"source_type must be one of: roost_boost, manual, iptv_url")
		return
	}

	// Validate M3U URL reachability when provided
	if req.M3UURL != nil && *req.M3UURL != "" {
		if err := probeURL(r.Context(), *req.M3UURL); err != nil {
			writeError(w, http.StatusBadRequest, "m3u_unreachable",
				"M3U URL is not reachable: "+err.Error())
			return
		}
	}

	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_stream_sources (name, source_type, m3u_url, roost_family_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		req.Name, req.SourceType, req.M3UURL, req.RoostFamilyID,
	).Scan(&id)
	if err != nil {
		log.Printf("[sports/sources] insert: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create source")
		return
	}

	source, err := s.getSourceByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch created source")
		return
	}
	writeJSON(w, http.StatusCreated, source)
}

// ─── GET /sports/sources ──────────────────────────────────────────────────────

// handleListSources lists all registered sources with health status and channel count.
// Query param: ?enabled=true to show only enabled sources (default).
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("enabled") != "false"

	query := `
		SELECT ss.id, ss.name, ss.source_type, ss.m3u_url, ss.roost_family_id,
		       ss.health_status, ss.last_health_check_at, ss.last_healthy_at,
		       ss.enabled, ss.created_at, ss.updated_at,
		       COUNT(sc.id) AS channel_count
		FROM sports_stream_sources ss
		LEFT JOIN sports_source_channels sc ON sc.source_id = ss.id`

	if enabledOnly {
		query += ` WHERE ss.enabled = true`
	}
	query += `
		GROUP BY ss.id
		ORDER BY ss.created_at DESC`

	rows, err := s.db.QueryContext(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list sources")
		return
	}
	defer rows.Close()

	sources := []StreamSource{}
	for rows.Next() {
		src := scanStreamSource(rows)
		sources = append(sources, src)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sources": sources, "count": len(sources)})
}

// ─── GET /sports/sources/{id} ─────────────────────────────────────────────────

// handleGetSource returns a single source with its channel list.
func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	source, err := s.getSourceByID(r.Context(), id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get source")
		return
	}

	// Fetch channels
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, source_id, channel_name, channel_url, group_title, tvg_id,
		       matched_league_id, match_confidence, match_confirmed, created_at
		FROM sports_source_channels
		WHERE source_id = $1
		ORDER BY match_confidence DESC, channel_name
		LIMIT 500`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get channels")
		return
	}
	defer rows.Close()

	channels := []SourceChannel{}
	for rows.Next() {
		var ch SourceChannel
		if err := rows.Scan(
			&ch.ID, &ch.SourceID, &ch.ChannelName, &ch.ChannelURL,
			&ch.GroupTitle, &ch.TVGID, &ch.MatchedLeagueID,
			&ch.MatchConfidence, &ch.MatchConfirmed, &ch.CreatedAt,
		); err != nil {
			continue
		}
		channels = append(channels, ch)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source":   source,
		"channels": channels,
	})
}

// ─── DELETE /sports/sources/{id} ─────────────────────────────────────────────

// handleDeleteSource soft-deletes a source by setting enabled = false.
// The row and historical assignments are preserved.
func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE sports_stream_sources SET enabled = false, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to delete source")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Source not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── POST /sports/sources/{id}/refresh ───────────────────────────────────────

// handleRefreshSource triggers an async M3U re-parse and channel re-match.
// Returns 202 immediately; the work runs in a goroutine.
func (s *Server) handleRefreshSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Verify source exists and is enabled
	var m3uURL sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT m3u_url FROM sports_stream_sources WHERE id = $1 AND enabled = true`, id,
	).Scan(&m3uURL)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Source not found or disabled")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch source")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.runChannelMatch(ctx, id); err != nil {
			log.Printf("[sports/sources] refresh source %s: %v", id, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":    "refresh_started",
		"source_id": id,
	})
}

// ─── GET /sports/sources/{id}/health ─────────────────────────────────────────

// handleGetSourceHealth returns the current health status for a source.
func (s *Server) handleGetSourceHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var (
		healthStatus       string
		lastHealthCheckAt  *time.Time
		lastHealthyAt      *time.Time
		channelCount       int
	)
	err := s.db.QueryRowContext(r.Context(), `
		SELECT ss.health_status, ss.last_health_check_at, ss.last_healthy_at,
		       COUNT(sc.id) AS channel_count
		FROM sports_stream_sources ss
		LEFT JOIN sports_source_channels sc ON sc.source_id = ss.id
		WHERE ss.id = $1
		GROUP BY ss.id`, id).Scan(
		&healthStatus, &lastHealthCheckAt, &lastHealthyAt, &channelCount,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get health")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source_id":            id,
		"health_status":        healthStatus,
		"last_health_check_at": lastHealthCheckAt,
		"last_healthy_at":      lastHealthyAt,
		"channel_count":        channelCount,
	})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// getSourceByID fetches a single StreamSource with channel count.
func (s *Server) getSourceByID(ctx context.Context, id string) (StreamSource, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT ss.id, ss.name, ss.source_type, ss.m3u_url, ss.roost_family_id,
		       ss.health_status, ss.last_health_check_at, ss.last_healthy_at,
		       ss.enabled, ss.created_at, ss.updated_at,
		       COUNT(sc.id) AS channel_count
		FROM sports_stream_sources ss
		LEFT JOIN sports_source_channels sc ON sc.source_id = ss.id
		WHERE ss.id = $1
		GROUP BY ss.id`, id)
	return scanStreamSourceRow(row)
}

// scanStreamSource scans one row from a multi-row query into a StreamSource.
func scanStreamSource(rows *sql.Rows) StreamSource {
	var src StreamSource
	_ = rows.Scan(
		&src.ID, &src.Name, &src.SourceType, &src.M3UURL, &src.RoostFamilyID,
		&src.HealthStatus, &src.LastHealthCheckAt, &src.LastHealthyAt,
		&src.Enabled, &src.CreatedAt, &src.UpdatedAt,
		&src.ChannelCount,
	)
	return src
}

// scanStreamSourceRow scans a single QueryRow into a StreamSource.
func scanStreamSourceRow(row *sql.Row) (StreamSource, error) {
	var src StreamSource
	err := row.Scan(
		&src.ID, &src.Name, &src.SourceType, &src.M3UURL, &src.RoostFamilyID,
		&src.HealthStatus, &src.LastHealthCheckAt, &src.LastHealthyAt,
		&src.Enabled, &src.CreatedAt, &src.UpdatedAt,
		&src.ChannelCount,
	)
	return src, err
}

// probeURL sends a HEAD request to verify URL reachability (5s timeout).
func probeURL(ctx context.Context, rawURL string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return &httpStatusError{status: resp.StatusCode}
	}
	return nil
}

type httpStatusError struct{ status int }

func (e *httpStatusError) Error() string {
	return "HTTP " + http.StatusText(e.status)
}
