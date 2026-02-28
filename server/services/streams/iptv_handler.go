// Package streams implements IPTV source management for family-contributed
// sources (Roost Boost pool). This is distinct from the admin-managed
// iptv_sources table; these are sources contributed by subscribing families
// to unlock the Boost live TV feature.
//
// Routes (all require authenticated subscriber session):
//
//	GET    /api/sources              — list sources for the authenticated family
//	POST   /api/sources              — add a new IPTV source
//	GET    /api/sources/{id}         — get source details (credentials masked)
//	PUT    /api/sources/{id}         — update source name or URL
//	DELETE /api/sources/{id}         — remove source and all derived channels
//	POST   /api/sources/validate     — validate an m3u8 URL before saving
package streams

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// DB is the interface used for database access. Both *sql.DB and *sql.Tx satisfy it.
type DB interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// IPTVSource is the API representation of a family IPTV source.
// Credentials are masked before returning to callers.
type IPTVSource struct {
	ID           string  `json:"id"`
	FamilyID     string  `json:"family_id"`
	Name         string  `json:"name"`
	M3U8URL      string  `json:"m3u8_url"`
	Username     string  `json:"username,omitempty"` // only present if set; password always omitted
	ChannelCount int     `json:"channel_count"`
	LastSyncAt   *string `json:"last_sync_at,omitempty"`
	HealthStatus string  `json:"health_status"`
	CreatedAt    string  `json:"created_at"`
}

// Handler handles IPTV source CRUD for the streams service.
type Handler struct {
	DB DB
}

// NewHandler creates a Handler backed by db.
func NewHandler(db DB) *Handler {
	return &Handler{DB: db}
}

// ServeHTTP dispatches to the correct sub-handler based on path and method.
// Register with: mux.Handle("/api/sources", h) and /api/sources/
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sources")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		h.ListSources(w, r)
	case path == "" && r.Method == http.MethodPost:
		h.CreateSource(w, r)
	case path == "/validate" && r.Method == http.MethodPost:
		h.ValidateSource(w, r)
	case strings.HasPrefix(path, "/") && !strings.Contains(path[1:], "/"):
		id := path[1:]
		switch r.Method {
		case http.MethodGet:
			h.GetSource(w, r, id)
		case http.MethodPut:
			h.UpdateSource(w, r, id)
		case http.MethodDelete:
			h.DeleteSource(w, r, id)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "method not supported"))
		}
	default:
		writeJSON(w, http.StatusNotFound, errResp("not_found", "endpoint not found"))
	}
}

// ListSources handles GET /api/sources.
// X-Family-ID header identifies the family.
func (h *Handler) ListSources(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "X-Family-ID header required"))
		return
	}

	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT id, family_id, name, m3u8_url, COALESCE(username,''),
		       channel_count, last_sync_at::text, health_status, created_at::text
		FROM family_iptv_sources
		WHERE family_id = $1
		ORDER BY created_at ASC
	`, familyID)
	if err != nil {
		log.Printf("[streams] list sources: db error: %v", err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "failed to query sources"))
		return
	}
	defer rows.Close()

	var sources []IPTVSource
	for rows.Next() {
		var s IPTVSource
		var lastSync *string
		if err := rows.Scan(&s.ID, &s.FamilyID, &s.Name, &s.M3U8URL, &s.Username,
			&s.ChannelCount, &lastSync, &s.HealthStatus, &s.CreatedAt); err != nil {
			log.Printf("[streams] list sources: scan error: %v", err)
			continue
		}
		s.LastSyncAt = lastSync
		sources = append(sources, s)
	}
	if sources == nil {
		sources = []IPTVSource{}
	}
	writeJSON(w, http.StatusOK, sources)
}

// CreateSource handles POST /api/sources.
// Body: { "name": "...", "m3u8_url": "...", "username": "...", "password": "..." }
func (h *Handler) CreateSource(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "X-Family-ID header required"))
		return
	}

	var req struct {
		Name     string `json:"name"`
		M3U8URL  string `json:"m3u8_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_json", "valid JSON body required"))
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing_field", "name is required"))
		return
	}
	if !isHTTPURL(req.M3U8URL) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_url", "m3u8_url must be a valid http(s) URL"))
		return
	}

	encPassword := ""
	if req.Password != "" {
		enc, err := encryptCredential(req.Password)
		if err != nil {
			log.Printf("[streams] create source: encrypt error: %v", err)
			writeJSON(w, http.StatusInternalServerError, errResp("encrypt_error", "credential encryption failed"))
			return
		}
		encPassword = enc
	}

	var id string
	err := h.DB.QueryRowContext(r.Context(), `
		INSERT INTO family_iptv_sources (family_id, name, m3u8_url, username, password)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, familyID, req.Name, req.M3U8URL, nullStr(req.Username), nullStr(encPassword)).Scan(&id)
	if err != nil {
		log.Printf("[streams] create source: db error: %v", err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "failed to create source"))
		return
	}

	// Trigger async channel sync — non-blocking.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := SyncSourceChannels(ctx, h.DB, id, req.M3U8URL); err != nil {
			log.Printf("[streams] create source: initial sync error (source=%s): %v", id, err)
		}
	}()

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "syncing"})
}

// GetSource handles GET /api/sources/{id}.
func (h *Handler) GetSource(w http.ResponseWriter, r *http.Request, id string) {
	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "X-Family-ID header required"))
		return
	}

	var s IPTVSource
	var lastSync *string
	err := h.DB.QueryRowContext(r.Context(), `
		SELECT id, family_id, name, m3u8_url, COALESCE(username,''),
		       channel_count, last_sync_at::text, health_status, created_at::text
		FROM family_iptv_sources
		WHERE id = $1 AND family_id = $2
	`, id, familyID).Scan(&s.ID, &s.FamilyID, &s.Name, &s.M3U8URL, &s.Username,
		&s.ChannelCount, &lastSync, &s.HealthStatus, &s.CreatedAt)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "source not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "failed to fetch source"))
		return
	}
	s.LastSyncAt = lastSync
	writeJSON(w, http.StatusOK, s)
}

// UpdateSource handles PUT /api/sources/{id}.
// Only name and m3u8_url may be updated; credentials require a new source for security.
func (h *Handler) UpdateSource(w http.ResponseWriter, r *http.Request, id string) {
	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "X-Family-ID header required"))
		return
	}

	var req struct {
		Name    string `json:"name"`
		M3U8URL string `json:"m3u8_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_json", "valid JSON body required"))
		return
	}
	if req.Name == "" && req.M3U8URL == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing_field", "name or m3u8_url required"))
		return
	}
	if req.M3U8URL != "" && !isHTTPURL(req.M3U8URL) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_url", "m3u8_url must be a valid http(s) URL"))
		return
	}

	res, err := h.DB.ExecContext(r.Context(), `
		UPDATE family_iptv_sources
		SET name = COALESCE(NULLIF($3,''), name),
		    m3u8_url = COALESCE(NULLIF($4,''), m3u8_url),
		    updated_at = NOW()
		WHERE id = $1 AND family_id = $2
	`, id, familyID, req.Name, req.M3U8URL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "update failed"))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "source not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DeleteSource handles DELETE /api/sources/{id}.
// Cascade deletes all derived channels.
func (h *Handler) DeleteSource(w http.ResponseWriter, r *http.Request, id string) {
	familyID := r.Header.Get("X-Family-ID")
	if familyID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "X-Family-ID header required"))
		return
	}

	res, err := h.DB.ExecContext(r.Context(), `
		DELETE FROM family_iptv_sources
		WHERE id = $1 AND family_id = $2
	`, id, familyID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "delete failed"))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "source not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ValidateSource handles POST /api/sources/validate.
// Fetches the M3U8 URL and returns channel count if valid, without saving anything.
// Body: { "m3u8_url": "...", "username": "...", "password": "..." }
func (h *Handler) ValidateSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		M3U8URL  string `json:"m3u8_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_json", "valid JSON body required"))
		return
	}
	if !isHTTPURL(req.M3U8URL) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_url", "m3u8_url must be a valid http(s) URL"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	channels, err := ParseM3U(ctx, req.M3U8URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("parse_error", fmt.Sprintf("failed to fetch/parse M3U: %v", err)))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":         true,
		"channel_count": len(channels),
		"sample": func() []string {
			names := make([]string, 0, 5)
			for i, ch := range channels {
				if i >= 5 {
					break
				}
				names = append(names, ch.Name)
			}
			return names
		}(),
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// SyncSourceChannels fetches and upserts channels for a family IPTV source.
// Called async after create; can also be called manually for refresh.
func SyncSourceChannels(ctx context.Context, db DB, sourceID, m3u8URL string) error {
	channels, err := ParseM3U(ctx, m3u8URL)
	if err != nil {
		return fmt.Errorf("sync: parse error: %w", err)
	}

	for _, ch := range channels {
		slug := slugify(ch.Name + "-" + ch.TvgID)
		_, err := db.ExecContext(ctx, `
			INSERT INTO family_iptv_channels
			  (source_id, slug, name, logo_url, group_title, tvg_id, stream_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (slug) DO UPDATE
			  SET name = EXCLUDED.name,
			      logo_url = EXCLUDED.logo_url,
			      group_title = EXCLUDED.group_title,
			      stream_url = EXCLUDED.stream_url
		`, sourceID, slug, ch.Name, nullStr(ch.LogoURL), nullStr(ch.GroupTitle),
			nullStr(ch.TvgID), ch.StreamURL)
		if err != nil {
			log.Printf("[streams] sync: upsert channel %q: %v", ch.Name, err)
		}
	}

	_, _ = db.ExecContext(ctx, `
		UPDATE family_iptv_sources
		SET channel_count = $1, last_sync_at = NOW(), health_status = 'healthy', updated_at = NOW()
		WHERE id = $2
	`, len(channels), sourceID)

	return nil
}

// encryptCredential encrypts plaintext using AES-256-GCM.
// Key is read from ROOST_ENCRYPTION_KEY (32-byte base64-encoded).
func encryptCredential(plaintext string) (string, error) {
	keyB64 := os.Getenv("ROOST_ENCRYPTION_KEY")
	if keyB64 == "" {
		return "", fmt.Errorf("ROOST_ENCRYPTION_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("ROOST_ENCRYPTION_KEY must be 32-byte base64-encoded")
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

// isHTTPURL returns true if s is a valid http or https URL.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// nullStr returns nil if s is empty, otherwise s — for nullable DB params.
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// slugify converts a string to a URL-safe lowercase slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			out.WriteByte('-')
		}
	}
	slug := strings.Trim(out.String(), "-")
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errResp creates a standard error response map.
func errResp(code, msg string) map[string]string {
	return map[string]string{"error": code, "message": msg}
}
