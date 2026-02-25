// handlers_stream.go — Stream gateway handlers (DB-wired, privacy-preserving).
// Phase FLOCKTV FTV.0.T05 / FTV.1.T04: validates subscriber JWT, looks up canonical_id
// in the shared acquisition pool, generates HMAC-signed Cloudflare CDN URLs (15-min TTL),
// and logs stream events without capturing IP addresses, device IDs, or any PII beyond family_id.
package flocktv

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// streamRequest is the POST /flocktv/stream body.
type streamRequest struct {
	FamilyID    string `json:"family_id"`
	CanonicalID string `json:"canonical_id"`
	Quality     string `json:"quality"`
}

// StreamResponse is the response containing a signed CDN manifest URL.
type StreamResponse struct {
	SignedURL    string `json:"signed_url"`
	ExpiresAt   int64  `json:"expires_at"`
	CDNManifest string `json:"cdn_manifest"`
	StreamID    string `json:"stream_id,omitempty"`
}

// streamStartRequest is the POST /flocktv/stream/start body.
type streamStartRequest struct {
	FamilyID    string `json:"family_id"`
	CanonicalID string `json:"canonical_id"`
	Quality     string `json:"quality"`
}

// streamEndRequest is the POST /flocktv/stream/end body.
type streamEndRequest struct {
	EventID string `json:"event_id"`
}

// generateSignedURL produces a Cloudflare-compatible HMAC-signed URL.
// path is the R2/CDN path, e.g., "/content/imdb:tt0111161/manifest.m3u8".
// ttl controls how long the signature is valid.
func generateSignedURL(cdnBase, hmacSecret, path string, ttl time.Duration) string {
	expiresAt := time.Now().Add(ttl).Unix()
	data := fmt.Sprintf("%s|%d", path, expiresAt)
	mac := hmac.New(sha256.New, []byte(hmacSecret))
	mac.Write([]byte(data))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s%s?expires=%d&sig=%s", cdnBase, path, expiresAt, sig)
}

// defaultQuality returns a normalised quality string, defaulting to "720p".
func defaultQuality(q string) string {
	valid := map[string]bool{"360p": true, "480p": true, "720p": true, "1080p": true, "4k": true}
	if valid[q] {
		return q
	}
	return "720p"
}

// handleStreamRequest validates access and returns a signed CDN manifest URL.
// POST /flocktv/stream
func (s *Server) handleStreamRequest(w http.ResponseWriter, r *http.Request) {
	var req streamRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		familyID = req.FamilyID
	}

	if familyID == "" || req.CanonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "family_id and canonical_id are required")
		return
	}

	quality := defaultQuality(req.Quality)

	// Verify content is available in the shared pool (if DB is wired).
	if s.db != nil {
		var acqStatus string
		err := s.db.QueryRowContext(r.Context(),
			`SELECT status FROM acquisition_queue WHERE canonical_id = $1 ORDER BY queued_at DESC LIMIT 1`,
			req.CanonicalID,
		).Scan(&acqStatus)
		if err == sql.ErrNoRows || (err == nil && acqStatus != "complete") {
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"status":       "processing",
				"canonical_id": req.CanonicalID,
				"message":      "content is being acquired, try again shortly",
			})
			return
		}
	}

	cdnBase := getEnv("CDN_RELAY_URL", "https://stream.yourflock.com")
	hmacSecret := os.Getenv("CDN_HMAC_SECRET")
	if hmacSecret == "" {
		s.logger.Error("CDN_HMAC_SECRET is not set — stream signing unavailable")
		writeError(w, http.StatusServiceUnavailable, "config_error", "stream signing not configured")
		return
	}

	path := fmt.Sprintf("/content/%s/%s/manifest.m3u8", req.CanonicalID, quality)
	signedURL := generateSignedURL(cdnBase, hmacSecret, path, 15*time.Minute)
	expiresAt := time.Now().Add(15 * time.Minute).Unix()

	// Log stream start for billing (privacy-safe: no IP, no device fingerprint).
	streamID := ""
	if s.db != nil {
		var sid string
		if insErr := s.db.QueryRowContext(r.Context(), `
			INSERT INTO stream_events (family_id, canonical_id, quality, started_at)
			VALUES ($1, $2, $3, NOW())
			RETURNING id`,
			familyID, req.CanonicalID, quality,
		).Scan(&sid); insErr == nil {
			streamID = sid
		}
	}

	writeJSON(w, http.StatusOK, StreamResponse{
		SignedURL:    signedURL,
		ExpiresAt:   expiresAt,
		CDNManifest: signedURL,
		StreamID:    streamID,
	})
}

// handleStreamStart logs the start of a stream event for billing aggregation.
// POST /flocktv/stream/start
// Privacy rule: NO IP address, NO device fingerprint, NO user-agent stored.
func (s *Server) handleStreamStart(w http.ResponseWriter, r *http.Request) {
	var req streamStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		familyID = req.FamilyID
	}

	if familyID == "" || req.CanonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "family_id and canonical_id are required")
		return
	}

	quality := defaultQuality(req.Quality)

	if s.db == nil {
		// Dev/test mode — no DB. Return a structurally valid transient event ID.
		writeJSON(w, http.StatusCreated, map[string]string{
			"event_id": uuid.New().String(),
			"status":   "started",
		})
		return
	}

	var eventID string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO stream_events (family_id, canonical_id, quality, started_at)
		VALUES ($1, $2, $3, NOW())
		RETURNING id`,
		familyID, req.CanonicalID, quality,
	).Scan(&eventID)

	if err != nil {
		s.logger.Error("stream start insert failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to log stream start")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"event_id": eventID,
		"status":   "started",
	})
}

// handleStreamEnd records the end of a stream event, enabling duration_sec calculation.
// POST /flocktv/stream/end
func (s *Server) handleStreamEnd(w http.ResponseWriter, r *http.Request) {
	var req streamEndRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.EventID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "event_id is required")
		return
	}

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":   "recorded",
			"event_id": req.EventID,
		})
		return
	}

	result, err := s.db.ExecContext(r.Context(), `
		UPDATE stream_events
		SET ended_at = NOW()
		WHERE id = $1 AND ended_at IS NULL`,
		req.EventID,
	)
	if err != nil {
		s.logger.Error("stream end update failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to record stream end")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "not_found", "event not found or already ended")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "recorded",
		"event_id": req.EventID,
	})
}

// handleUsageSummary returns the current family's stream usage for the billing period.
// GET /flocktv/usage
func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	familyID := familyIDFromCtx(r.Context())
	if familyID == "" {
		familyID = r.URL.Query().Get("family_id")
	}
	if familyID == "" {
		writeError(w, http.StatusUnauthorized, "missing_family", "family_id required")
		return
	}

	billingMonth := time.Now().UTC().Format("2006-01")

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"this_month_hours": 0.0,
			"base_charge":      4.99,
			"usage_charge":     0.00,
			"total_charge":     4.99,
			"billing_month":    billingMonth,
		})
		return
	}

	var streamHours, baseCharge, usageCharge, totalCharge float64

	err := s.db.QueryRowContext(r.Context(), `
		SELECT stream_hours, base_charge, usage_charge, total_charge
		FROM monthly_stream_hours
		WHERE family_id = $1 AND billing_month = date_trunc('month', NOW())`,
		familyID,
	).Scan(&streamHours, &baseCharge, &usageCharge, &totalCharge)

	if err != nil {
		// Fall back to real-time aggregation.
		var totalSeconds int64
		_ = s.db.QueryRowContext(r.Context(), `
			SELECT COALESCE(SUM(duration_sec), 0)
			FROM stream_events
			WHERE family_id = $1
			  AND started_at >= date_trunc('month', NOW())
			  AND duration_sec IS NOT NULL`,
			familyID,
		).Scan(&totalSeconds)

		streamHours = float64(totalSeconds) / 3600.0
		usage := FamilyUsage{FamilyID: familyID, StreamHours: streamHours}
		charged := CalculateCharge(usage)
		baseCharge = charged.BaseCharge
		usageCharge = charged.UsageCharge
		totalCharge = charged.TotalCharge
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"family_id":        familyID,
		"this_month_hours": streamHours,
		"base_charge":      baseCharge,
		"usage_charge":     usageCharge,
		"total_charge":     totalCharge,
		"billing_month":    billingMonth,
	})
}

// handleCatalog returns a paginated view of available shared content.
// GET /flocktv/catalog?type=movie|show|music|game&page=N
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	contentType := r.URL.Query().Get("type")
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n >= 1 {
			page = n
		}
	}
	limit := 50
	offset := (page - 1) * limit

	type CatalogItem struct {
		CanonicalID string  `json:"canonical_id"`
		ContentType string  `json:"content_type"`
		Status      string  `json:"status"`
		R2Path      *string `json:"r2_path,omitempty"`
	}

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": []CatalogItem{},
			"page":  page,
			"type":  contentType,
		})
		return
	}

	var rows *sql.Rows
	var queryErr error

	if contentType != "" && validContentTypes[contentType] {
		rows, queryErr = s.db.QueryContext(r.Context(), `
			SELECT canonical_id, content_type, status, r2_path
			FROM acquisition_queue
			WHERE content_type = $1 AND status = 'complete'
			ORDER BY completed_at DESC
			LIMIT $2 OFFSET $3`,
			contentType, limit, offset)
	} else {
		rows, queryErr = s.db.QueryContext(r.Context(), `
			SELECT canonical_id, content_type, status, r2_path
			FROM acquisition_queue
			WHERE status = 'complete'
			ORDER BY completed_at DESC
			LIMIT $1 OFFSET $2`,
			limit, offset)
	}

	if queryErr != nil {
		s.logger.Error("catalog query failed", "error", queryErr.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to retrieve catalog")
		return
	}
	defer rows.Close()

	items := []CatalogItem{}
	for rows.Next() {
		var item CatalogItem
		if scanErr := rows.Scan(&item.CanonicalID, &item.ContentType, &item.Status, &item.R2Path); scanErr == nil {
			items = append(items, item)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":  items,
		"page":   page,
		"limit":  limit,
		"offset": offset,
	})
}

// Ensure os is imported (CDN_HMAC_SECRET).
var _ = os.Getenv
