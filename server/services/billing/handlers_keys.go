// handlers_keys.go — HLS encryption key management endpoints.
// P16-T03: Content Protection
//
// POST /admin/keys/rotate-channel/:channelID
//   Generates a new AES-128 HLS key for the given channel, invalidates the old
//   key, and returns the new key ID. Superowner only.
package billing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
)

// handleRotateChannelKey handles POST /admin/keys/rotate-channel/:channelID.
func (s *Server) handleRotateChannelKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	// Extract channelID from path: /admin/keys/rotate-channel/{channelID}
	channelID := strings.TrimPrefix(r.URL.Path, "/admin/keys/rotate-channel/")
	channelID = strings.Trim(channelID, "/")
	if channelID == "" {
		writeError(w, http.StatusBadRequest, "missing_channel_id", "Channel ID required in path")
		return
	}

	// Verify channel exists.
	var exists bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM channels WHERE id = $1)`, channelID).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "channel_not_found", "Channel not found")
		return
	}

	// Generate new 16-byte AES-128 key.
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "key_generation_failed", "Failed to generate key")
		return
	}
	keyID := generateHLSKeyID()
	keyHex := hex.EncodeToString(keyBytes)

	// Upsert key — replaces existing key for this channel.
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO hls_channel_keys (channel_id, key_id, key_bytes, active, created_at)
		VALUES ($1, $2, $3, true, NOW())
		ON CONFLICT (channel_id) DO UPDATE SET
			key_id     = EXCLUDED.key_id,
			key_bytes  = EXCLUDED.key_bytes,
			active     = true,
			rotated_at = NOW()
	`, channelID, keyID, keyHex)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			writeError(w, http.StatusServiceUnavailable, "migration_pending",
				"hls_channel_keys table not yet created — run migration 029_hls_keys.sql")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to store key")
		return
	}

	// Audit log.
	claims := auth.ClaimsFromContext(r.Context())
	actorID := ""
	if claims != nil {
		actorID = claims.Subject
	}
	s.logAdminAction(r, actorID, "key.rotate", "channel", channelID, map[string]interface{}{
		"key_id": keyID,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"key_id":     keyID,
		"channel_id": channelID,
		"rotated_at": time.Now().UTC().Format(time.RFC3339),
		"message":    "Key rotated. New streams will use the new key immediately.",
	})
}

// generateHLSKeyID creates a random 32-hex-char key identifier.
func generateHLSKeyID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
