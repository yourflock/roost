// handler.go — HTTP handlers for Roost Boost IPTV pool contributions.
//
// Subscriber routes (require session token):
//
//	POST   /api/boost/contribute       — contribute family IPTV stream to pool
//	DELETE /api/boost/contribute       — remove contribution from pool
//	GET    /api/boost/status           — user's contribution status + channel count
//
// Admin routes (require superowner JWT):
//
//	GET    /admin/boost/pool           — list all active contributors (no credentials)
package boost

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// BoostHandlers provides HTTP handlers for the Boost API.
type BoostHandlers struct {
	DB   *sql.DB
	Pool *BoostPool
}

// NewBoostHandlers creates BoostHandlers backed by db and pool.
func NewBoostHandlers(db *sql.DB, pool *BoostPool) *BoostHandlers {
	return &BoostHandlers{DB: db, Pool: pool}
}

// HandleContribute handles POST /api/boost/contribute.
// A subscriber registers their IPTV source with the Boost pool.
// Body: { "source_id": "uuid" } — references an existing family_iptv_sources row.
func (h *BoostHandlers) HandleContribute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBoostError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	subscriberID := r.Header.Get("X-Subscriber-ID")
	familyID := r.Header.Get("X-Family-ID")
	if subscriberID == "" || familyID == "" {
		writeBoostError(w, http.StatusUnauthorized, "unauthorized",
			"X-Subscriber-ID and X-Family-ID required")
		return
	}

	var req struct {
		SourceID string `json:"source_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SourceID == "" {
		writeBoostError(w, http.StatusBadRequest, "invalid_body", "source_id required")
		return
	}

	// Verify the source belongs to this family.
	var m3u8URL, name string
	var channelCount int
	err := h.DB.QueryRowContext(r.Context(), `
		SELECT m3u8_url, name, channel_count
		FROM family_iptv_sources
		WHERE id = $1 AND family_id = $2
	`, req.SourceID, familyID).Scan(&m3u8URL, &name, &channelCount)
	if err == sql.ErrNoRows {
		writeBoostError(w, http.StatusNotFound, "not_found", "IPTV source not found")
		return
	}
	if err != nil {
		writeBoostError(w, http.StatusInternalServerError, "db_error", "lookup failed")
		return
	}
	if channelCount == 0 {
		writeBoostError(w, http.StatusBadRequest, "not_synced",
			"source has no channels yet — wait for sync to complete before contributing")
		return
	}

	// Upsert boost_contributions record.
	_, err = h.DB.ExecContext(r.Context(), `
		INSERT INTO boost_contributions (family_id, subscriber_id, source_id, contributed_at, is_active)
		VALUES ($1, $2, $3, NOW(), true)
		ON CONFLICT (family_id) DO UPDATE
		  SET source_id     = EXCLUDED.source_id,
		      contributed_at = NOW(),
		      is_active      = true
	`, familyID, subscriberID, req.SourceID)
	if err != nil {
		log.Printf("[boost] contribute insert: %v", err)
		writeBoostError(w, http.StatusInternalServerError, "db_error", "failed to record contribution")
		return
	}

	// Load channels for this source and add to in-memory pool.
	channels, _ := h.loadChannelSlugs(r.Context(), req.SourceID)
	h.Pool.Contribute(&ContributedStream{
		ID:            req.SourceID,
		FamilyID:      familyID,
		M3U8URL:       m3u8URL,
		Channels:      channels,
		ChannelCount:  channelCount,
		HealthStatus:  "healthy",
		ContributedAt: time.Now(),
		Active:        true,
	})

	log.Printf("[boost] family %s contributed source %s (%d channels)", familyID, req.SourceID, channelCount)

	writeBoostJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "contributed",
		"channel_count": channelCount,
		"source_name":   name,
	})
}

// HandleRemoveContribution handles DELETE /api/boost/contribute.
func (h *BoostHandlers) HandleRemoveContribution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeBoostError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeBoostError(w, http.StatusUnauthorized, "unauthorized", "X-Family-ID required")
		return
	}

	var sourceID string
	err := h.DB.QueryRowContext(r.Context(), `
		UPDATE boost_contributions
		SET is_active = false
		WHERE family_id = $1 AND is_active = true
		RETURNING source_id
	`, familyID).Scan(&sourceID)
	if err == sql.ErrNoRows {
		writeBoostError(w, http.StatusNotFound, "not_found", "no active contribution found")
		return
	}
	if err != nil {
		writeBoostError(w, http.StatusInternalServerError, "db_error", "update failed")
		return
	}

	h.Pool.Remove(sourceID)
	w.WriteHeader(http.StatusNoContent)
}

// HandleBoostStatus handles GET /api/boost/status.
func (h *BoostHandlers) HandleBoostStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBoostError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeBoostError(w, http.StatusUnauthorized, "unauthorized", "X-Family-ID required")
		return
	}

	var isActive bool
	var sourceID string
	var channelCount int
	var contributedAt *string

	err := h.DB.QueryRowContext(r.Context(), `
		SELECT bc.is_active, bc.source_id,
		       COALESCE(fis.channel_count, 0), bc.contributed_at::text
		FROM boost_contributions bc
		LEFT JOIN family_iptv_sources fis ON fis.id = bc.source_id
		WHERE bc.family_id = $1
		ORDER BY bc.contributed_at DESC
		LIMIT 1
	`, familyID).Scan(&isActive, &sourceID, &channelCount, &contributedAt)
	if err == sql.ErrNoRows {
		writeBoostJSON(w, http.StatusOK, map[string]interface{}{
			"contributing":  false,
			"channel_count": 0,
		})
		return
	}
	if err != nil {
		writeBoostError(w, http.StatusInternalServerError, "db_error", "lookup failed")
		return
	}

	resp := map[string]interface{}{
		"contributing":   isActive,
		"source_id":      sourceID,
		"channel_count":  channelCount,
		"contributed_at": contributedAt,
	}
	writeBoostJSON(w, http.StatusOK, resp)
}

// HandleAdminPoolList handles GET /admin/boost/pool.
// Returns all active contributors (without credentials) for admin view.
func (h *BoostHandlers) HandleAdminPoolList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBoostError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	contributors := h.Pool.ListContributors()
	if contributors == nil {
		contributors = []*ContributedStream{}
	}
	writeBoostJSON(w, http.StatusOK, map[string]interface{}{
		"pool_size":    h.Pool.Size(),
		"contributors": contributors,
	})
}

// ── DB helpers ────────────────────────────────────────────────────────────────

// loadChannelSlugs fetches all channel slugs for a source from the DB.
func (h *BoostHandlers) loadChannelSlugs(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT slug FROM family_iptv_channels WHERE source_id = $1`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			slugs = append(slugs, s)
		}
	}
	return slugs, rows.Err()
}

// ── Response helpers ──────────────────────────────────────────────────────────

func writeBoostJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeBoostError(w http.ResponseWriter, status int, code, msg string) {
	writeBoostJSON(w, status, map[string]string{"error": code, "message": msg})
}
