// roost_boost.go — Roost Boost pool integration endpoints.
// OSG.3.002: Handle family IPTV contributions to the shared sports pool.
// Endpoints are service-to-service (Roost backend → Sports service) and require
// NSELF_SERVICE_TOKEN bearer auth.
package sports

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// notifyBaseURL is scaffolded for the future reverse-direction notification call
// (sports → Roost when a family.s contribution is first used).
// Set via ROOST_BASE_URL env var.
var notifyBaseURL = func() string {
	if v := os.Getenv("ROOST_BASE_URL"); v != "" {
		return v
	}
	return "https://api.roost.unity.dev"
}()

// ─── POST /sports/roost-boost/join ───────────────────────────────────────────

// handleRoostBoostJoin is called by the Roost backend when a family activates Roost Boost.
// Creates a sports_stream_sources row with source_type='roost_boost' and triggers
// async channel matching.
func (s *Server) handleRoostBoostJoin(w http.ResponseWriter, r *http.Request) {
	if !checkServiceToken(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing service token")
		return
	}

	var req struct {
		FamilyID   string `json:"family_id"`
		IPTVМ3UURL string `json:"iptv_m3u_url"`
		IPTVEPGUrl string `json:"iptv_epg_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.FamilyID == "" || req.IPTVМ3UURL == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "family_id and iptv_m3u_url are required")
		return
	}

	// Validate M3U URL reachability before inserting
	if err := probeURL(r.Context(), req.IPTVМ3UURL); err != nil {
		writeError(w, http.StatusBadRequest, "m3u_unreachable",
			"IPTV M3U URL is not reachable: "+err.Error())
		return
	}

	// Check if this family already has a source (may be rejoining after leave)
	var existingID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id FROM sports_stream_sources WHERE roost_family_id = $1 LIMIT 1`,
		req.FamilyID,
	).Scan(&existingID)

	if err == nil {
		// Re-enable existing source and update the M3U URL
		_, updateErr := s.db.ExecContext(r.Context(), `
			UPDATE sports_stream_sources
			SET m3u_url = $1, enabled = true, health_status = 'unknown', updated_at = now()
			WHERE id = $2`, req.IPTVМ3UURL, existingID)
		if updateErr != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Failed to re-enable source")
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := s.runChannelMatch(ctx, existingID); err != nil {
				log.Printf("[sports/boost] re-match source %s: %v", existingID, err)
			}
		}()
		writeJSON(w, http.StatusCreated, map[string]string{"source_id": existingID, "status": "rejoined"})
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to check existing source")
		return
	}

	// Insert new roost_boost source
	sourceName := "Roost Boost — Family " + req.FamilyID[:8]
	var sourceID string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_stream_sources (name, source_type, m3u_url, roost_family_id)
		VALUES ($1, 'roost_boost', $2, $3)
		RETURNING id`,
		sourceName, req.IPTVМ3UURL, req.FamilyID,
	).Scan(&sourceID)
	if err != nil {
		log.Printf("[sports/boost] insert source for family %s: %v", req.FamilyID, err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create source")
		return
	}

	// Trigger async channel matching
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.runChannelMatch(ctx, sourceID); err != nil {
			log.Printf("[sports/boost] channel match for new source %s: %v", sourceID, err)
		}
	}()

	log.Printf("[sports/boost] family %s joined pool, source %s created", req.FamilyID, sourceID)
	writeJSON(w, http.StatusCreated, map[string]string{"source_id": sourceID, "status": "joined"})
}

// ─── DELETE /sports/roost-boost/leave ────────────────────────────────────────

// handleRoostBoostLeave is called when a family disables Roost Boost or downgrades.
// Soft-deletes their source (enabled=false). Row is preserved for history.
func (s *Server) handleRoostBoostLeave(w http.ResponseWriter, r *http.Request) {
	if !checkServiceToken(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing service token")
		return
	}

	var req struct {
		FamilyID string `json:"family_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.FamilyID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "family_id is required")
		return
	}

	res, err := s.db.ExecContext(r.Context(), `
		UPDATE sports_stream_sources
		SET enabled = false, updated_at = now()
		WHERE roost_family_id = $1 AND enabled = true`, req.FamilyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to leave pool")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "No active Roost Boost source found for this family")
		return
	}

	log.Printf("[sports/boost] family %s left pool", req.FamilyID)
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /sports/roost-boost/stats ───────────────────────────────────────────

// handleRoostBoostStats returns aggregate pool statistics. No auth required.
func (s *Server) handleRoostBoostStats(w http.ResponseWriter, r *http.Request) {
	var totalMembers, totalChannels, healthyCount, degradedCount, downCount int

	err := s.db.QueryRowContext(r.Context(), `
		SELECT
		  COUNT(DISTINCT ss.id) FILTER (WHERE ss.enabled = true)                      AS total_members,
		  COUNT(DISTINCT sc.id)                                                        AS total_channels,
		  COUNT(DISTINCT ss.id) FILTER (WHERE ss.health_status = 'healthy')           AS healthy_count,
		  COUNT(DISTINCT ss.id) FILTER (WHERE ss.health_status = 'degraded')          AS degraded_count,
		  COUNT(DISTINCT ss.id) FILTER (WHERE ss.health_status = 'down')              AS down_count
		FROM sports_stream_sources ss
		LEFT JOIN sports_source_channels sc ON sc.source_id = ss.id
		WHERE ss.source_type = 'roost_boost'`,
	).Scan(&totalMembers, &totalChannels, &healthyCount, &degradedCount, &downCount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get pool stats")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_members":  totalMembers,
		"total_channels": totalChannels,
		"healthy_count":  healthyCount,
		"degraded_count": degradedCount,
		"down_count":     downCount,
	})
}

// ─── Auth helper ─────────────────────────────────────────────────────────────

// checkServiceToken verifies the NSELF_SERVICE_TOKEN bearer token.
// Returns true if the token is present and matches.
func checkServiceToken(r *http.Request) bool {
	expected := os.Getenv("NSELF_SERVICE_TOKEN")
	if expected == "" {
		// Token not configured — allow in development
		return true
	}
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	return token == expected
}
