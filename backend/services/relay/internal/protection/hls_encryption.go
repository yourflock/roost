// hls_encryption.go — AES-128 HLS encryption key management.
// P16-T03: Content Protection
//
// HLS content encryption prevents direct stream URL sharing — even if a
// subscriber leaks their stream URL, segments cannot be played without
// the corresponding AES key from the key server. The key server validates
// the subscriber's session token on every key request, so revoked subscribers
// immediately lose playback.
//
// Per-channel keys are stored in the database (hls_keys table created by
// 026_audit_log.sql's companion migration). Key rotation is triggered via
// POST /admin/keys/rotate-channel/:channelID from the billing admin API.
package protection

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HLSKey represents an active AES-128 encryption key for a channel.
type HLSKey struct {
	KeyID     string    // UUID stored in DB
	KeyBytes  []byte    // 16-byte AES-128 key
	ChannelID string    // which channel this key protects
	KeyURI    string    // URI Owl/Roost serves in #EXT-X-KEY
	CreatedAt time.Time
}

// GenerateHLSKey creates a new AES-128 key for a channel and persists it.
// Returns the key bytes, key ID, and the key URI to embed in the HLS playlist.
//
// The key URI points to GET /keys/{keyID} — the KeyServer handler validates
// the subscriber's session token before returning the 16 raw bytes.
//
// After generation, upsert into hls_channel_keys:
//
//	INSERT INTO hls_channel_keys (channel_id, key_id, key_bytes, active)
//	VALUES ($1, $2, $3, true)
//	ON CONFLICT (channel_id) DO UPDATE SET key_id=$2, key_bytes=$3, active=true, rotated_at=NOW()
func GenerateHLSKey(db *sql.DB, channelID, baseURL string) (*HLSKey, error) {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("hls key: failed to generate random bytes: %w", err)
	}

	keyID := generateKeyID()
	keyURI := strings.TrimRight(baseURL, "/") + "/keys/" + keyID

	_, err := db.Exec(`
		INSERT INTO hls_channel_keys (channel_id, key_id, key_bytes, active, created_at)
		VALUES ($1, $2, $3, true, NOW())
		ON CONFLICT (channel_id) DO UPDATE SET
			key_id       = EXCLUDED.key_id,
			key_bytes    = EXCLUDED.key_bytes,
			active       = true,
			rotated_at   = NOW()
	`, channelID, keyID, hex.EncodeToString(keyBytes))
	if err != nil {
		return nil, fmt.Errorf("hls key: db upsert: %w", err)
	}

	return &HLSKey{
		KeyID:     keyID,
		KeyBytes:  keyBytes,
		ChannelID: channelID,
		KeyURI:    keyURI,
		CreatedAt: time.Now(),
	}, nil
}

// KeyServer returns an http.Handler that serves HLS decryption keys.
// The handler validates the subscriber's Bearer session token before returning
// the raw 16-byte AES key. Unauthenticated or expired requests receive 401.
//
// Route: GET /keys/:keyID
func KeyServer(db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract keyID from path: /keys/{keyID}
		keyID := strings.TrimPrefix(r.URL.Path, "/keys/")
		keyID = strings.Trim(keyID, "/")
		if keyID == "" {
			http.Error(w, "missing key id", http.StatusBadRequest)
			return
		}

		// Validate subscriber session token.
		// The subscriber must have an active owl_session (not expired, not revoked).
		token := extractBearerToken(r)
		if token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		var sessionActive bool
		err := db.QueryRowContext(r.Context(), `
			SELECT EXISTS(
				SELECT 1 FROM owl_sessions
				WHERE token_hash = encode(sha256($1::bytea), 'hex')
				  AND expires_at > NOW()
				  AND revoked_at IS NULL
			)
		`, token).Scan(&sessionActive)
		if err != nil || !sessionActive {
			http.Error(w, "invalid or expired session", http.StatusUnauthorized)
			return
		}

		// Fetch the key bytes for this keyID.
		var keyHex string
		err = db.QueryRowContext(r.Context(), `
			SELECT key_bytes FROM hls_channel_keys WHERE key_id = $1 AND active = true
		`, keyID).Scan(&keyHex)
		if err == sql.ErrNoRows {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		keyBytes, err := hex.DecodeString(keyHex)
		if err != nil || len(keyBytes) != 16 {
			http.Error(w, "key format error", http.StatusInternalServerError)
			return
		}

		// Return raw 16-byte AES key. HLS players expect application/octet-stream.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Length", "16")
		w.WriteHeader(http.StatusOK)
		w.Write(keyBytes)
	})
}

// generateKeyID generates a random 32-hex-character key identifier.
func generateKeyID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// extractBearerToken pulls the Bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Also accept token as query param for HLS players that can't set headers.
	return r.URL.Query().Get("token")
}
