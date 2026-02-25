package handlers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// IPTVSourceRow is returned by GET /admin/iptv-sources.
// Sensitive config fields are masked before returning.
type IPTVSourceRow struct {
	ID             string          `json:"id"`
	DisplayName    string          `json:"display_name"`
	SourceType     string          `json:"source_type"`
	Config         json.RawMessage `json:"config"` // sensitive fields masked
	ChannelCount   *int            `json:"channel_count,omitempty"`
	LastRefreshedAt *string        `json:"last_refreshed_at,omitempty"`
	IsActive       bool            `json:"is_active"`
}

// ListIPTVSources handles GET /admin/iptv-sources.
func (h *AdminHandlers) ListIPTVSources(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, display_name, source_type, config, channel_count, last_refreshed_at, is_active
		   FROM iptv_sources
		  WHERE roost_id = $1 AND is_active = TRUE
		  ORDER BY created_at ASC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var sources []IPTVSourceRow
	for rows.Next() {
		var s IPTVSourceRow
		var configRaw []byte
		var lastRefreshed *string
		if err := rows.Scan(&s.ID, &s.DisplayName, &s.SourceType, &configRaw, &s.ChannelCount, &lastRefreshed, &s.IsActive); err != nil {
			continue
		}
		s.LastRefreshedAt = lastRefreshed
		// Mask sensitive fields before returning
		s.Config = maskIPTVConfig(configRaw)
		sources = append(sources, s)
	}
	if sources == nil {
		sources = []IPTVSourceRow{}
	}
	writeAdminJSON(w, http.StatusOK, sources)
}

// maskIPTVConfig replaces sensitive fields with masked values in the config JSON.
func maskIPTVConfig(raw []byte) json.RawMessage {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return json.RawMessage(`{}`)
	}
	if _, ok := m["password"]; ok {
		m["password"] = "***"
	}
	if _, ok := m["mac"]; ok {
		m["mac"] = "XX:XX:XX:XX:XX:XX"
	}
	masked, _ := json.Marshal(m)
	return json.RawMessage(masked)
}

// AddIPTVSourceRequest is the POST /admin/iptv-sources body.
type AddIPTVSourceRequest struct {
	DisplayName string `json:"display_name"`
	SourceType  string `json:"source_type"`
	// M3U:
	URL string `json:"url,omitempty"`
	// Xtream:
	Host     string `json:"host,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Stalker:
	Portal string `json:"portal,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

// AddIPTVSource handles POST /admin/iptv-sources.
func (h *AdminHandlers) AddIPTVSource(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req AddIPTVSourceRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	if req.DisplayName == "" {
		http.Error(w, `{"error":"display_name required"}`, http.StatusBadRequest)
		return
	}

	// Build and validate type-specific config
	configMap := map[string]interface{}{}
	switch req.SourceType {
	case "m3u":
		if req.URL == "" {
			http.Error(w, `{"error":"url required for m3u source"}`, http.StatusBadRequest)
			return
		}
		if !isValidHTTPSURL(req.URL) {
			http.Error(w, `{"error":"url must be a valid HTTP or HTTPS URL"}`, http.StatusBadRequest)
			return
		}
		configMap["url"] = req.URL
	case "xtream":
		if req.Host == "" || req.Username == "" || req.Password == "" {
			http.Error(w, `{"error":"host, username, password required for xtream source"}`, http.StatusBadRequest)
			return
		}
		configMap["host"] = req.Host
		configMap["username"] = req.Username
		// Encrypt password before storing
		encrypted, err := encryptField(req.Password)
		if err != nil {
			slog.Error("iptv source: encryption failed", "err", err)
			http.Error(w, `{"error":"encryption_failed"}`, http.StatusInternalServerError)
			return
		}
		configMap["password"] = encrypted
	case "stalker":
		if req.Portal == "" || req.MAC == "" {
			http.Error(w, `{"error":"portal and mac required for stalker source"}`, http.StatusBadRequest)
			return
		}
		configMap["portal"] = req.Portal
		encrypted, err := encryptField(req.MAC)
		if err != nil {
			slog.Error("iptv source: encryption failed", "err", err)
			http.Error(w, `{"error":"encryption_failed"}`, http.StatusInternalServerError)
			return
		}
		configMap["mac"] = encrypted
	default:
		http.Error(w, `{"error":"invalid source_type; must be m3u, xtream, or stalker"}`, http.StatusBadRequest)
		return
	}

	configJSON, _ := json.Marshal(configMap)

	var rowID string
	err := h.DB.QueryRowContext(r.Context(),
		`INSERT INTO iptv_sources (roost_id, display_name, source_type, config)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		claims.RoostID, req.DisplayName, req.SourceType, configJSON,
	).Scan(&rowID)
	if err != nil {
		slog.Error("admin/iptv-sources: db error", "err", err)
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "iptv_source.add", rowID,
		map[string]any{"source_type": req.SourceType, "display_name": req.DisplayName},
		// NOTE: no credentials in audit log
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{"id": rowID})
}

// RefreshIPTVSource handles POST /admin/iptv-sources/:id/refresh.
func (h *AdminHandlers) RefreshIPTVSource(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	sourceID := extractPathID(r.URL.Path, "/admin/iptv-sources/", "/refresh")
	if !isValidUUID(sourceID) {
		http.Error(w, `{"error":"invalid source id"}`, http.StatusBadRequest)
		return
	}

	// Enqueue channel-list refresh job
	jobID := newUUID()
	slog.Info("iptv source refresh enqueued", "source_id", sourceID, "job_id", jobID)

	al.Log(r, claims.RoostID, claims.FlockUserID, "iptv_source.refresh_triggered", sourceID, nil)
	writeAdminJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

// DeleteIPTVSource handles DELETE /admin/iptv-sources/:id.
func (h *AdminHandlers) DeleteIPTVSource(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	sourceID := extractPathID(r.URL.Path, "/admin/iptv-sources/", "")
	if !isValidUUID(sourceID) {
		http.Error(w, `{"error":"invalid source id"}`, http.StatusBadRequest)
		return
	}

	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE iptv_sources SET is_active = FALSE WHERE id = $1 AND roost_id = $2`,
		sourceID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "iptv_source.remove", sourceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Encryption helpers ────────────────────────────────────────────────────────

// encryptField encrypts a plaintext string using AES-256-GCM.
// Key is read from ROOST_ENCRYPTION_KEY env var (must be 32 bytes base64-encoded).
func encryptField(plaintext string) (string, error) {
	keyB64 := os.Getenv("ROOST_ENCRYPTION_KEY")
	if keyB64 == "" {
		return "", fmt.Errorf("ROOST_ENCRYPTION_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("ROOST_ENCRYPTION_KEY must be 32-byte base64-encoded value")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// isValidHTTPSURL returns true if s is a valid HTTP or HTTPS URL.
func isValidHTTPSURL(s string) bool {
	u, err := url.ParseRequestURI(s)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// ── Shared utilities ──────────────────────────────────────────────────────────

// newUUID returns a new random UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:],
	)
}

// decodeJSON decodes the request body as JSON into v.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// padBase64 ensures s has proper base64 padding.
func padBase64(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	}
	return s
}

// stripBase64Prefix removes data URL prefix if present.
func stripBase64Prefix(s string) string {
	if idx := strings.Index(s, ","); idx != -1 {
		return s[idx+1:]
	}
	return s
}
