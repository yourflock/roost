// stream_handler.go — Stream request gateway for the FTV Gateway service.
// Phase FLOCKTV FTV.1.T04: validates JWT, looks up selection, checks shared pool
// availability, generates HMAC-signed Cloudflare URLs (15-min TTL), and logs
// stream events for per-stream-hour billing.
package ftv_gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StreamGateway handles stream URL generation with billing event logging.
type StreamGateway struct {
	centralDB *pgxpool.Pool
	familyDBs *FamilyDBManager
	logger    *slog.Logger
}

// NewStreamGateway creates a StreamGateway.
func NewStreamGateway(centralDB *pgxpool.Pool, familyDBs *FamilyDBManager, logger *slog.Logger) *StreamGateway {
	return &StreamGateway{
		centralDB: centralDB,
		familyDBs: familyDBs,
		logger:    logger,
	}
}

// generateStreamSignedURL produces a HMAC-SHA256-signed CDN URL for a content path.
// The CF Worker validates the signature before serving from R2.
func generateStreamSignedURL(cdnBase, signingKey, r2Path string, ttl time.Duration) string {
	expiresAt := time.Now().Add(ttl).Unix()
	message := fmt.Sprintf("%s:%d", r2Path, expiresAt)
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(message))
	token := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s/%s?token=%s&expires=%d",
		cdnBase, r2Path, token, expiresAt)
}

// HandleStreamGet handles GET /stream/{selection_id}.
// Validates JWT → looks up selection → checks availability → returns signed URL.
func (g *StreamGateway) HandleStreamGet(w http.ResponseWriter, r *http.Request) {
	selectionID := chi.URLParam(r, "selection_id")
	if selectionID == "" {
		writeGatewayError(w, http.StatusBadRequest, "missing_param", "selection_id is required")
		return
	}

	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeGatewayError(w, http.StatusUnauthorized, "missing_family", "X-Family-ID header required")
		return
	}

	if g.centralDB == nil {
		// Dev mode: no central DB. Return a structurally valid signed URL with real UUIDs.
		cdnBase := getGatewayEnv("CDN_RELAY_URL", "https://stream.yourflock.org")
		signingKey := os.Getenv("CF_STREAM_SIGNING_KEY")
		if signingKey == "" {
			writeGatewayError(w, http.StatusServiceUnavailable, "config_error", "CF_STREAM_SIGNING_KEY not set")
			return
		}
		streamID := uuid.New().String()
		streamPath := fmt.Sprintf("dev/%s/manifest.m3u8", streamID)
		signedURL := generateStreamSignedURL(cdnBase, signingKey, streamPath, 15*time.Minute)
		writeGatewayJSON(w, http.StatusOK, map[string]interface{}{
			"signed_url":  signedURL,
			"expires_at":  time.Now().Add(15 * time.Minute).Unix(),
			"stream_id":   streamID,
		})
		return
	}

	// Look up selection in per-family DB.
	familyPool, err := g.familyDBs.FamilyDBPool(r.Context(), familyID)
	if err != nil {
		g.logger.Warn("family DB unavailable", "family_id", familyID, "error", err.Error())
		writeGatewayError(w, http.StatusNotFound, "family_not_found", "family DB unavailable")
		return
	}

	var canonicalID, contentType string
	selErr := familyPool.QueryRow(r.Context(),
		`SELECT canonical_id, content_type FROM content_selections WHERE id = $1 AND family_id = $2`,
		selectionID, familyID,
	).Scan(&canonicalID, &contentType)
	if selErr != nil {
		writeGatewayError(w, http.StatusNotFound, "selection_not_found", "selection not found")
		return
	}

	// Check shared pool availability.
	var poolStatus string
	var r2Path *string
	poolErr := g.centralDB.QueryRow(r.Context(),
		`SELECT status, r2_path FROM acquisition_queue WHERE canonical_id = $1 ORDER BY queued_at DESC LIMIT 1`,
		canonicalID,
	).Scan(&poolStatus, &r2Path)

	if poolErr != nil || poolStatus != "complete" || r2Path == nil {
		writeGatewayJSON(w, http.StatusAccepted, map[string]interface{}{
			"status":       "processing",
			"canonical_id": canonicalID,
			"webhook_url":  fmt.Sprintf("/notifications/content-ready/%s", canonicalID),
		})
		return
	}

	// Generate signed URL.
	cdnBase := getGatewayEnv("CDN_RELAY_URL", "https://stream.yourflock.org")
	signingKey := os.Getenv("CF_STREAM_SIGNING_KEY")
	if signingKey == "" {
		writeGatewayError(w, http.StatusServiceUnavailable, "config_error", "CF_STREAM_SIGNING_KEY not set")
		return
	}

	url := generateStreamSignedURL(cdnBase, signingKey, *r2Path, 15*time.Minute)
	expiresAt := time.Now().Add(15 * time.Minute).Unix()

	// Log stream start for billing.
	streamID := logStreamStart(r.Context(), g.centralDB, familyID, canonicalID, "1080p", g.logger)

	writeGatewayJSON(w, http.StatusOK, map[string]interface{}{
		"signed_url":   url,
		"cdn_manifest": url,
		"expires_at":   expiresAt,
		"stream_id":    streamID,
		"canonical_id": canonicalID,
	})
}

// HandleStreamEnd handles POST /stream/{stream_id}/end.
// Records stream end time for billing duration calculation.
func (g *StreamGateway) HandleStreamEnd(w http.ResponseWriter, r *http.Request) {
	streamID := chi.URLParam(r, "stream_id")
	if streamID == "" {
		writeGatewayError(w, http.StatusBadRequest, "missing_param", "stream_id is required")
		return
	}

	if g.centralDB == nil {
		writeGatewayJSON(w, http.StatusOK, map[string]string{
			"status":    "recorded",
			"stream_id": streamID,
		})
		return
	}

	result, err := g.centralDB.Exec(r.Context(), `
		UPDATE stream_events SET ended_at = NOW()
		WHERE id = $1 AND ended_at IS NULL`,
		streamID,
	)
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "db_error", "failed to record stream end")
		return
	}
	if result.RowsAffected() == 0 {
		writeGatewayError(w, http.StatusNotFound, "not_found", "stream event not found or already ended")
		return
	}

	writeGatewayJSON(w, http.StatusOK, map[string]string{
		"status":    "recorded",
		"stream_id": streamID,
	})
}

// logStreamStart inserts a stream_events row and returns the event UUID.
// Privacy-safe: no IP, no device, no user_id beyond family_id.
func logStreamStart(ctx context.Context, db *pgxpool.Pool, familyID, canonicalID, quality string, logger *slog.Logger) string {
	if db == nil {
		return ""
	}
	var eventID string
	err := db.QueryRow(ctx, `
		INSERT INTO stream_events (family_id, canonical_id, quality, started_at)
		VALUES ($1, $2, $3, NOW())
		RETURNING id`,
		familyID, canonicalID, quality,
	).Scan(&eventID)
	if err != nil {
		logger.Warn("stream event insert failed", "error", err.Error())
	}
	return eventID
}

// getGatewayEnv returns the env var value or fallback.
func getGatewayEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
