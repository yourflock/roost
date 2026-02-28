// main.go — Roost Catalog Service.
// Manages channels, categories, EPG sources, and featured lists.
// Port: 8095 (env: CATALOG_PORT). Internal service proxied via Nginx.
//
// Admin routes (require superowner JWT):
//   POST   /admin/channels
//   GET    /admin/channels
//   GET    /admin/channels/:id
//   PUT    /admin/channels/:id
//   DELETE /admin/channels/:id          — soft delete (is_active=false)
//   POST   /admin/channels/:id/logo     — multipart logo upload
//   GET    /logos/:filename             — serve logo files
//   POST   /admin/categories
//   GET    /admin/categories
//   PUT    /admin/categories/:id
//   DELETE /admin/categories/:id        — fails if channels reference it
//   GET    /admin/epg-sources
//   POST   /admin/epg-sources
//   PUT    /admin/epg-sources/:id
//   DELETE /admin/epg-sources/:id
//   POST   /admin/epg-sources/:id/sync  — trigger manual sync
//   GET    /admin/featured-lists
//   GET    /admin/featured-lists/:slug/channels
//   PUT    /admin/featured-lists/:slug/channels
//   DELETE /admin/featured-lists/:slug/channels/:channel_id
//   PATCH  /admin/featured-lists/:slug
//
// Public routes (subscriber session token OR no auth):
//   GET /channels/search        — full-text + filter search
//   GET /owl/featured           — featured lists for Owl clients
//   GET /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	rootauth "github.com/unyeco/roost/internal/auth"
)

// ---- helpers ----------------------------------------------------------------

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func connectDB() (*sql.DB, error) {
	dsn := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return db, nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// requireSuperowner middleware: validates Bearer JWT and checks is_superowner=true.
func requireSuperowner(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" {
			writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header required")
			return
		}
		parts := strings.SplitN(h, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			writeError(w, http.StatusUnauthorized, "invalid_token", "Bearer token required")
			return
		}
		claims, err := rootauth.ValidateAccessToken(strings.TrimSpace(parts[1]))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token", "Invalid or expired token")
			return
		}

		var isSuperowner bool
		_ = db.QueryRowContext(r.Context(),
			`SELECT is_superowner FROM subscribers WHERE id = $1`, claims.Subject).Scan(&isSuperowner)
		if !isSuperowner {
			writeError(w, http.StatusForbidden, "forbidden", "superowner access required")
			return
		}
		next(w, r)
	}
}

// ---- server -----------------------------------------------------------------

type server struct {
	db      *sql.DB
	logoDir string
}

// ---- channel types ----------------------------------------------------------

type channelResponse struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Slug          string      `json:"slug"`
	CategoryID    *string     `json:"category_id"`
	LogoURL       *string     `json:"logo_url"`
	IsActive      bool        `json:"is_active"`
	LanguageCode  *string     `json:"language_code"`
	RegionCode    *string     `json:"region_code,omitempty"`
	BitrateConfig interface{} `json:"bitrate_config"`
	EpgChannelID  *string     `json:"epg_channel_id"`
	SortOrder     int         `json:"sort_order"`
	CreatedAt     time.Time   `json:"created_at"`
}

type channelInput struct {
	Name          string      `json:"name"`
	Slug          string      `json:"slug"`
	CategoryID    *string     `json:"category_id"`
	LogoURL       *string     `json:"logo_url"`
	SourceURL     string      `json:"source_url"` // accepted in admin input, never returned
	SourceType    string      `json:"source_type"`
	LanguageCode  *string     `json:"language_code"`
	CountryCode   *string     `json:"country_code"`
	BitrateConfig interface{} `json:"bitrate_config"`
	EpgChannelID  *string     `json:"epg_channel_id"`
	IsActive      *bool       `json:"is_active"`
	SortOrder     *int        `json:"sort_order"`
}

// scanChannel reads a channel row into channelResponse. NEVER reads source_url.
func scanChannel(row interface {
	Scan(...interface{}) error
}) (*channelResponse, error) {
	var c channelResponse
	var bitrateRaw []byte
	err := row.Scan(
		&c.ID, &c.Name, &c.Slug, &c.CategoryID, &c.LogoURL,
		&c.IsActive, &c.LanguageCode, &c.RegionCode, &bitrateRaw,
		&c.EpgChannelID, &c.SortOrder, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if bitrateRaw != nil {
		_ = json.Unmarshal(bitrateRaw, &c.BitrateConfig)
	}
	return &c, nil
}

const channelSelectCols = `
	id, name, slug, category_id, logo_url,
	is_active, language_code, country_code, bitrate_config,
	epg_channel_id, sort_order, created_at`

// ---- handlers: channels -----------------------------------------------------

// POST /admin/channels
func (s *server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var inp channelInput
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
		return
	}
	if strings.TrimSpace(inp.Name) == "" || strings.TrimSpace(inp.Slug) == "" || strings.TrimSpace(inp.SourceURL) == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "name, slug, and source_url are required")
		return
	}
	if inp.SourceType == "" {
		inp.SourceType = "hls"
	}
	isActive := true
	if inp.IsActive != nil {
		isActive = *inp.IsActive
	}
	sortOrder := 0
	if inp.SortOrder != nil {
		sortOrder = *inp.SortOrder
	}

	var bitrateJSON []byte
	if inp.BitrateConfig != nil {
		bitrateJSON, _ = json.Marshal(inp.BitrateConfig)
	}

	row := s.db.QueryRowContext(r.Context(), `
		INSERT INTO channels (name, slug, category_id, logo_url, source_url, source_type,
			language_code, country_code, bitrate_config, epg_channel_id, is_active, sort_order)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+channelSelectCols,
		inp.Name, inp.Slug, inp.CategoryID, inp.LogoURL, inp.SourceURL, inp.SourceType,
		inp.LanguageCode, inp.CountryCode, bitrateJSON, inp.EpgChannelID, isActive, sortOrder,
	)
	ch, err := scanChannel(row)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "slug_conflict", "A channel with that slug already exists")
			return
		}
		log.Printf("[catalog] create channel: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create channel")
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

// GET /admin/channels
func (s *server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	q := r.URL.Query()
	limit := 50
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	offset := 0
	if o := q.Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	// Build filter
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if catID := q.Get("category_id"); catID != "" {
		where = append(where, fmt.Sprintf("category_id = $%d", argIdx))
		args = append(args, catID)
		argIdx++
	}
	if active := q.Get("active"); active != "" {
		bv := active == "true"
		where = append(where, fmt.Sprintf("is_active = $%d", argIdx))
		args = append(args, bv)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	// Count
	var total int
	_ = s.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COUNT(*) FROM channels %s`, whereClause), args...).Scan(&total)

	// Page
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT %s FROM channels %s ORDER BY sort_order, name LIMIT $%d OFFSET $%d`,
		channelSelectCols, whereClause, argIdx, argIdx+1)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		log.Printf("[catalog] list channels: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list channels")
		return
	}
	defer rows.Close()

	channels := []*channelResponse{}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			log.Printf("[catalog] scan channel: %v", err)
			continue
		}
		channels = append(channels, ch)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"channels": channels, "total": total, "limit": limit, "offset": offset})
}

// GET /admin/channels/:id
func (s *server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	id := pathSegment(r.URL.Path, 3) // /admin/channels/{id}
	row := s.db.QueryRowContext(r.Context(),
		`SELECT `+channelSelectCols+` FROM channels WHERE id = $1`, id)
	ch, err := scanChannel(row)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get channel")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// PUT /admin/channels/:id
func (s *server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT required")
		return
	}
	id := pathSegment(r.URL.Path, 3)
	var inp channelInput
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
		return
	}

	// Build dynamic update
	sets := []string{}
	args := []interface{}{}
	argIdx := 1

	if inp.Name != "" {
		sets = append(sets, fmt.Sprintf("name=$%d", argIdx)); args = append(args, inp.Name); argIdx++
	}
	if inp.Slug != "" {
		sets = append(sets, fmt.Sprintf("slug=$%d", argIdx)); args = append(args, inp.Slug); argIdx++
	}
	if inp.CategoryID != nil {
		sets = append(sets, fmt.Sprintf("category_id=$%d", argIdx)); args = append(args, inp.CategoryID); argIdx++
	}
	if inp.LogoURL != nil {
		sets = append(sets, fmt.Sprintf("logo_url=$%d", argIdx)); args = append(args, inp.LogoURL); argIdx++
	}
	if inp.SourceURL != "" {
		sets = append(sets, fmt.Sprintf("source_url=$%d", argIdx)); args = append(args, inp.SourceURL); argIdx++
	}
	if inp.SourceType != "" {
		sets = append(sets, fmt.Sprintf("source_type=$%d", argIdx)); args = append(args, inp.SourceType); argIdx++
	}
	if inp.LanguageCode != nil {
		sets = append(sets, fmt.Sprintf("language_code=$%d", argIdx)); args = append(args, inp.LanguageCode); argIdx++
	}
	if inp.CountryCode != nil {
		sets = append(sets, fmt.Sprintf("country_code=$%d", argIdx)); args = append(args, inp.CountryCode); argIdx++
	}
	if inp.BitrateConfig != nil {
		b, _ := json.Marshal(inp.BitrateConfig)
		sets = append(sets, fmt.Sprintf("bitrate_config=$%d", argIdx)); args = append(args, b); argIdx++
	}
	if inp.EpgChannelID != nil {
		sets = append(sets, fmt.Sprintf("epg_channel_id=$%d", argIdx)); args = append(args, inp.EpgChannelID); argIdx++
	}
	if inp.IsActive != nil {
		sets = append(sets, fmt.Sprintf("is_active=$%d", argIdx)); args = append(args, *inp.IsActive); argIdx++
	}
	if inp.SortOrder != nil {
		sets = append(sets, fmt.Sprintf("sort_order=$%d", argIdx)); args = append(args, *inp.SortOrder); argIdx++
	}

	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "No fields to update")
		return
	}

	args = append(args, id)
	query := fmt.Sprintf(`UPDATE channels SET %s WHERE id=$%d RETURNING `+channelSelectCols,
		strings.Join(sets, ","), argIdx)
	row := s.db.QueryRowContext(r.Context(), query, args...)
	ch, err := scanChannel(row)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Channel not found")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "slug_conflict", "A channel with that slug already exists")
			return
		}
		log.Printf("[catalog] update channel: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update channel")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// DELETE /admin/channels/:id — soft delete (set is_active=false)
func (s *server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}
	id := pathSegment(r.URL.Path, 3)
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE channels SET is_active=false WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to delete channel")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /admin/channels/:id/logo
func (s *server) handleUploadLogo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	id := pathSegment(r.URL.Path, 3) // /admin/channels/{id}/logo

	// 2MB limit
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)

	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "multipart/form-data required")
		return
	}

	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "multipart_error", "Failed to parse multipart")
			return
		}
		if part.FormName() != "logo" {
			continue
		}

		// Determine extension from content type
		partCT := part.Header.Get("Content-Type")
		var ext string
		switch partCT {
		case "image/png":
			ext = ".png"
		case "image/jpeg", "image/jpg":
			ext = ".jpg"
		case "image/svg+xml":
			ext = ".svg"
		default:
			// Try filename
			fn := part.FileName()
			ext = strings.ToLower(filepath.Ext(fn))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".svg" {
				writeError(w, http.StatusBadRequest, "invalid_file_type", "Only PNG, JPG, and SVG files are accepted")
				return
			}
		}

		filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
		dst := filepath.Join(s.logoDir, filename)

		if err := os.MkdirAll(s.logoDir, 0755); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Failed to create logo directory")
			return
		}

		data, err := io.ReadAll(part)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read_error", "Failed to read file data (exceeds 2MB limit?)")
			return
		}
		if len(data) > 2<<20 {
			writeError(w, http.StatusBadRequest, "file_too_large", "Logo must be 2MB or smaller")
			return
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Failed to save logo")
			return
		}

		logoURL := "/logos/" + filename
		_, err = s.db.ExecContext(r.Context(),
			`UPDATE channels SET logo_url=$1 WHERE id=$2`, logoURL, id)
		if err != nil {
			_ = os.Remove(dst)
			writeError(w, http.StatusInternalServerError, "db_error", "Failed to update logo URL")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"logo_url": logoURL})
		return
	}
	writeError(w, http.StatusBadRequest, "missing_file", "No 'logo' file part found in multipart body")
}

// GET /logos/:filename
func (s *server) handleServeLogo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	filename := pathSegment(r.URL.Path, 2) // /logos/{filename}
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		writeError(w, http.StatusBadRequest, "invalid_filename", "Invalid filename")
		return
	}
	http.ServeFile(w, r, filepath.Join(s.logoDir, filename))
}

// ---- handlers: categories ---------------------------------------------------

type categoryResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// POST /admin/categories
func (s *server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var inp struct {
		Name      string `json:"name"`
		SortOrder *int   `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
		return
	}
	if strings.TrimSpace(inp.Name) == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "name is required")
		return
	}
	sortOrder := 0
	if inp.SortOrder != nil {
		sortOrder = *inp.SortOrder
	}
	var cat categoryResponse
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO channel_categories (name, sort_order) VALUES ($1,$2) RETURNING id, name, sort_order, created_at`,
		inp.Name, sortOrder).Scan(&cat.ID, &cat.Name, &cat.SortOrder, &cat.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "name_conflict", "A category with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create category")
		return
	}
	writeJSON(w, http.StatusCreated, cat)
}

// GET /admin/categories
func (s *server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, name, sort_order, created_at FROM channel_categories ORDER BY sort_order, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list categories")
		return
	}
	defer rows.Close()
	cats := []categoryResponse{}
	for rows.Next() {
		var cat categoryResponse
		if err := rows.Scan(&cat.ID, &cat.Name, &cat.SortOrder, &cat.CreatedAt); err == nil {
			cats = append(cats, cat)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"categories": cats})
}

// PUT /admin/categories/:id
func (s *server) handleUpdateCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT required")
		return
	}
	id := pathSegment(r.URL.Path, 3)
	var inp struct {
		Name      string `json:"name"`
		SortOrder *int   `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON")
		return
	}
	sets := []string{}
	args := []interface{}{}
	argIdx := 1
	if inp.Name != "" {
		sets = append(sets, fmt.Sprintf("name=$%d", argIdx)); args = append(args, inp.Name); argIdx++
	}
	if inp.SortOrder != nil {
		sets = append(sets, fmt.Sprintf("sort_order=$%d", argIdx)); args = append(args, *inp.SortOrder); argIdx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "No fields to update")
		return
	}
	args = append(args, id)
	var cat categoryResponse
	err := s.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`UPDATE channel_categories SET %s WHERE id=$%d RETURNING id,name,sort_order,created_at`,
			strings.Join(sets, ","), argIdx), args...).Scan(&cat.ID, &cat.Name, &cat.SortOrder, &cat.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Category not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update category")
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

// DELETE /admin/categories/:id — fails if channels reference it
func (s *server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}
	id := pathSegment(r.URL.Path, 3)

	var count int
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM channels WHERE category_id=$1`, id).Scan(&count)
	if count > 0 {
		writeError(w, http.StatusConflict, "has_channels",
			fmt.Sprintf("Cannot delete category: %d channel(s) reference it", count))
		return
	}

	res, err := s.db.ExecContext(r.Context(), `DELETE FROM channel_categories WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to delete category")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Category not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- handlers: EPG sources --------------------------------------------------

type epgSourceResponse struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name"`
	URL                     string     `json:"url"` // URL is not a stream source — safe to expose
	Priority                int        `json:"priority"`
	RefreshIntervalSeconds  int        `json:"refresh_interval_seconds"`
	IsActive                bool       `json:"is_active"`
	LastSyncAt              *time.Time `json:"last_sync_at"`
	CreatedAt               time.Time  `json:"created_at"`
}

func scanEpgSource(row interface{ Scan(...interface{}) error }) (*epgSourceResponse, error) {
	var s epgSourceResponse
	return &s, row.Scan(&s.ID, &s.Name, &s.URL, &s.Priority, &s.RefreshIntervalSeconds, &s.IsActive, &s.LastSyncAt, &s.CreatedAt)
}

const epgSourceCols = `id, name, url, priority, refresh_interval_seconds, is_active, last_sync_at, created_at`

// GET /admin/epg-sources
func (s *server) handleListEpgSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required"); return
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT `+epgSourceCols+` FROM epg_sources ORDER BY priority DESC, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list EPG sources"); return
	}
	defer rows.Close()
	sources := []*epgSourceResponse{}
	for rows.Next() {
		src, _ := scanEpgSource(rows)
		if src != nil {
			sources = append(sources, src)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sources": sources})
}

// POST /admin/epg-sources
func (s *server) handleCreateEpgSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required"); return
	}
	var inp struct {
		Name                   string `json:"name"`
		URL                    string `json:"url"`
		Priority               *int   `json:"priority"`
		RefreshIntervalSeconds *int   `json:"refresh_interval_seconds"`
		IsActive               *bool  `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON"); return
	}
	if inp.Name == "" || inp.URL == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "name and url required"); return
	}
	priority := 0
	if inp.Priority != nil {
		priority = *inp.Priority
	}
	interval := 21600
	if inp.RefreshIntervalSeconds != nil {
		interval = *inp.RefreshIntervalSeconds
	}
	active := true
	if inp.IsActive != nil {
		active = *inp.IsActive
	}
	row := s.db.QueryRowContext(r.Context(),
		`INSERT INTO epg_sources (name, url, priority, refresh_interval_seconds, is_active)
		 VALUES ($1,$2,$3,$4,$5) RETURNING `+epgSourceCols,
		inp.Name, inp.URL, priority, interval, active)
	src, err := scanEpgSource(row)
	if err != nil {
		log.Printf("[catalog] create epg source: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create EPG source"); return
	}
	writeJSON(w, http.StatusCreated, src)
}

// PUT /admin/epg-sources/:id
func (s *server) handleUpdateEpgSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT required"); return
	}
	id := pathSegment(r.URL.Path, 3)
	var inp struct {
		Name                   string `json:"name"`
		URL                    string `json:"url"`
		Priority               *int   `json:"priority"`
		RefreshIntervalSeconds *int   `json:"refresh_interval_seconds"`
		IsActive               *bool  `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON"); return
	}
	sets := []string{}
	args := []interface{}{}
	argIdx := 1
	if inp.Name != "" {
		sets = append(sets, fmt.Sprintf("name=$%d", argIdx)); args = append(args, inp.Name); argIdx++
	}
	if inp.URL != "" {
		sets = append(sets, fmt.Sprintf("url=$%d", argIdx)); args = append(args, inp.URL); argIdx++
	}
	if inp.Priority != nil {
		sets = append(sets, fmt.Sprintf("priority=$%d", argIdx)); args = append(args, *inp.Priority); argIdx++
	}
	if inp.RefreshIntervalSeconds != nil {
		sets = append(sets, fmt.Sprintf("refresh_interval_seconds=$%d", argIdx)); args = append(args, *inp.RefreshIntervalSeconds); argIdx++
	}
	if inp.IsActive != nil {
		sets = append(sets, fmt.Sprintf("is_active=$%d", argIdx)); args = append(args, *inp.IsActive); argIdx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "No fields to update"); return
	}
	args = append(args, id)
	row := s.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`UPDATE epg_sources SET %s WHERE id=$%d RETURNING `+epgSourceCols,
			strings.Join(sets, ","), argIdx), args...)
	src, err := scanEpgSource(row)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "EPG source not found"); return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update EPG source"); return
	}
	writeJSON(w, http.StatusOK, src)
}

// DELETE /admin/epg-sources/:id
func (s *server) handleDeleteEpgSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required"); return
	}
	id := pathSegment(r.URL.Path, 3)
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM epg_sources WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to delete EPG source"); return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "EPG source not found"); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /admin/epg-sources/:id/sync — triggers EPG sync for one source (notifies epg service)
func (s *server) handleTriggerEpgSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required"); return
	}
	id := pathSegment(r.URL.Path, 3) // /admin/epg-sources/{id}/sync

	epgServiceURL := getEnv("EPG_SERVICE_URL", "http://localhost:8096")
	resp, err := http.Post(fmt.Sprintf("%s/internal/sync-source?id=%s", epgServiceURL, id), "application/json", nil)
	if err != nil || resp.StatusCode >= 400 {
		// If EPG service is unreachable, update last_sync_at as a signal
		writeError(w, http.StatusServiceUnavailable, "epg_unavailable",
			"EPG service unavailable — sync could not be triggered")
		return
	}
	defer resp.Body.Close()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync_triggered", "source_id": id})
}

// ---- handlers: featured lists -----------------------------------------------

// GET /admin/featured-lists
func (s *server) handleListFeaturedLists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required"); return
	}
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, name, slug, description, is_active, sort_order, created_at
		 FROM channel_feature_lists ORDER BY sort_order, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list featured lists"); return
	}
	defer rows.Close()
	type listResp struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Slug        string    `json:"slug"`
		Description *string   `json:"description"`
		IsActive    bool      `json:"is_active"`
		SortOrder   int       `json:"sort_order"`
		CreatedAt   time.Time `json:"created_at"`
	}
	lists := []listResp{}
	for rows.Next() {
		var l listResp
		if err := rows.Scan(&l.ID, &l.Name, &l.Slug, &l.Description, &l.IsActive, &l.SortOrder, &l.CreatedAt); err == nil {
			lists = append(lists, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"lists": lists})
}

// GET /admin/featured-lists/:slug/channels
func (s *server) handleGetFeaturedListChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required"); return
	}
	slug := pathSegment(r.URL.Path, 3) // /admin/featured-lists/{slug}/channels

	// Resolve list ID
	var listID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id FROM channel_feature_lists WHERE slug=$1`, slug).Scan(&listID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "List not found"); return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get list"); return
	}

	type entryResp struct {
		EntryID   string    `json:"entry_id"`
		Position  int       `json:"position"`
		CreatedAt time.Time `json:"created_at"`
		Channel   map[string]interface{} `json:"channel"`
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT e.id, e.position, e.created_at,
		       c.id, c.name, c.slug, c.logo_url, c.is_active
		FROM channel_feature_entries e
		JOIN channels c ON c.id = e.channel_id
		WHERE e.list_id = $1
		ORDER BY e.position`, listID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get entries"); return
	}
	defer rows.Close()
	entries := []entryResp{}
	for rows.Next() {
		var e entryResp
		var cID, cName, cSlug string
		var cLogo *string
		var cActive bool
		if err := rows.Scan(&e.EntryID, &e.Position, &e.CreatedAt, &cID, &cName, &cSlug, &cLogo, &cActive); err == nil {
			e.Channel = map[string]interface{}{"id": cID, "name": cName, "slug": cSlug, "logo_url": cLogo, "is_active": cActive}
			entries = append(entries, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// PUT /admin/featured-lists/:slug/channels — replace entire list atomically
func (s *server) handleReplaceFeaturedListChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT required"); return
	}
	slug := pathSegment(r.URL.Path, 3)

	var body struct {
		ChannelIDs []string `json:"channel_ids"` // ordered list — position = index+1
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON"); return
	}

	var listID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id FROM channel_feature_lists WHERE slug=$1`, slug).Scan(&listID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "List not found"); return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get list"); return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to start transaction"); return
	}
	defer tx.Rollback()

	// Delete existing entries
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM channel_feature_entries WHERE list_id=$1`, listID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to clear list"); return
	}

	// Insert new entries
	for i, chID := range body.ChannelIDs {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO channel_feature_entries (list_id, channel_id, position) VALUES ($1,$2,$3)`,
			listID, chID, i+1); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("Failed to insert entry at position %d", i+1)); return
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to commit"); return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "updated", "count": len(body.ChannelIDs)})
}

// DELETE /admin/featured-lists/:slug/channels/:channel_id
func (s *server) handleRemoveFeaturedEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required"); return
	}
	// /admin/featured-lists/{slug}/channels/{channel_id}
	slug := pathSegment(r.URL.Path, 3)
	channelID := pathSegment(r.URL.Path, 5)

	var listID string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id FROM channel_feature_lists WHERE slug=$1`, slug).Scan(&listID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "List not found"); return
	}

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM channel_feature_entries WHERE list_id=$1 AND channel_id=$2`, listID, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to remove entry"); return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Entry not found"); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// PATCH /admin/featured-lists/:slug
func (s *server) handlePatchFeaturedList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required"); return
	}
	slug := pathSegment(r.URL.Path, 3)
	var inp struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IsActive    *bool  `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON"); return
	}
	sets := []string{}
	args := []interface{}{}
	argIdx := 1
	if inp.Name != "" {
		sets = append(sets, fmt.Sprintf("name=$%d", argIdx)); args = append(args, inp.Name); argIdx++
	}
	if inp.Description != "" {
		sets = append(sets, fmt.Sprintf("description=$%d", argIdx)); args = append(args, inp.Description); argIdx++
	}
	if inp.IsActive != nil {
		sets = append(sets, fmt.Sprintf("is_active=$%d", argIdx)); args = append(args, *inp.IsActive); argIdx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "No fields to update"); return
	}
	args = append(args, slug)
	res, err := s.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE channel_feature_lists SET %s WHERE slug=$%d`, strings.Join(sets, ","), argIdx), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update list"); return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "List not found"); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ---- handlers: channel search -----------------------------------------------

// GET /channels/search
func (s *server) handleChannelSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required"); return
	}
	q := r.URL.Query()
	searchQ := q.Get("q")
	categoryID := q.Get("category_id")
	language := q.Get("language")
	limit := 20
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	offset := 0
	if o := q.Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	where := []string{"is_active = true"}
	args := []interface{}{}
	argIdx := 1

	if searchQ != "" {
		// Use full-text search if search_vector column exists, fall back to ILIKE
		where = append(where, fmt.Sprintf("(name ILIKE $%d OR slug ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+searchQ+"%")
		argIdx++
	}
	if categoryID != "" {
		where = append(where, fmt.Sprintf("category_id=$%d", argIdx))
		args = append(args, categoryID)
		argIdx++
	}
	if language != "" {
		where = append(where, fmt.Sprintf("language_code=$%d", argIdx))
		args = append(args, language)
		argIdx++
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	var total int
	_ = s.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COUNT(*) FROM channels %s`, whereClause), args...).Scan(&total)

	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT %s FROM channels %s ORDER BY sort_order, name LIMIT $%d OFFSET $%d`,
			channelSelectCols, whereClause, argIdx, argIdx+1), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Search failed"); return
	}
	defer rows.Close()
	channels := []*channelResponse{}
	for rows.Next() {
		if ch, err := scanChannel(rows); err == nil {
			channels = append(channels, ch)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channels": channels,
		"total":    total,
		"query":    searchQ,
		"limit":    limit,
		"offset":   offset,
	})
}

// ---- handlers: owl featured (session-token or open) -------------------------

// GET /owl/featured — for Owl clients
func (s *server) handleOwlFeatured(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required"); return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT l.id, l.name, l.slug,
		       c.id, c.name, c.slug, c.logo_url
		FROM channel_feature_lists l
		JOIN channel_feature_entries e ON e.list_id = l.id
		JOIN channels c ON c.id = e.channel_id AND c.is_active = true
		WHERE l.is_active = true
		ORDER BY l.sort_order, l.name, e.position`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get featured lists"); return
	}
	defer rows.Close()

	type channelItem struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Slug    string  `json:"slug"`
		LogoURL *string `json:"logo_url"`
	}
	type listItem struct {
		Slug     string        `json:"slug"`
		Name     string        `json:"name"`
		Channels []channelItem `json:"channels"`
	}

	listMap := map[string]*listItem{}
	listOrder := []string{}
	for rows.Next() {
		var lID, lName, lSlug, cID, cName, cSlug string
		var cLogoURL *string
		if err := rows.Scan(&lID, &lName, &lSlug, &cID, &cName, &cSlug, &cLogoURL); err != nil {
			continue
		}
		if _, ok := listMap[lID]; !ok {
			listMap[lID] = &listItem{Slug: lSlug, Name: lName, Channels: []channelItem{}}
			listOrder = append(listOrder, lID)
		}
		listMap[lID].Channels = append(listMap[lID].Channels, channelItem{
			ID: cID, Name: cName, Slug: cSlug, LogoURL: cLogoURL,
		})
	}

	result := make([]*listItem, 0, len(listOrder))
	for _, id := range listOrder {
		result = append(result, listMap[id])
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"lists": result})
}

// ---- handlers: health -------------------------------------------------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var channelCount int
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM channels`).Scan(&channelCount)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"service":  "roost-catalog",
		"channels": channelCount,
	})
}

// ---- routing helper ---------------------------------------------------------

// pathSegment returns the n-th slash-delimited segment (0-indexed) of a URL path.
// e.g. pathSegment("/admin/channels/abc123", 2) = "abc123"
func pathSegment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n >= len(parts) {
		return ""
	}
	return parts[n]
}

// ---- main -------------------------------------------------------------------

func main() {
	port := getEnv("CATALOG_PORT", "8095")
	logoDir := getEnv("LOGO_DIR", "/var/roost/logos")

	db, err := connectDB()
	if err != nil {
		log.Fatalf("[catalog] db connect: %v", err)
	}
	defer db.Close()
	log.Printf("[catalog] database connected")

	s := &server{db: db, logoDir: logoDir}

	mux := http.NewServeMux()

	// Health — no auth
	mux.HandleFunc("/health", s.handleHealth)

	// Logo serving — no auth (logos are public assets)
	mux.HandleFunc("/logos/", s.handleServeLogo)

	// Public search — no auth required
	mux.HandleFunc("/channels/search", s.handleChannelSearch)

	// Owl featured — open for Owl clients (token checked by relay if needed)
	mux.HandleFunc("/owl/featured", s.handleOwlFeatured)

	// Admin: channels
	mux.HandleFunc("/admin/channels", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.handleCreateChannel(w, r)
		case http.MethodGet:
			s.handleListChannels(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST or GET required")
		}
	}))
	mux.HandleFunc("/admin/channels/", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		// Disambiguate: /admin/channels/{id} vs /admin/channels/{id}/logo
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// segments: [admin, channels, {id}] or [admin, channels, {id}, logo]
		if len(segments) == 4 && segments[3] == "logo" {
			s.handleUploadLogo(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.handleGetChannel(w, r)
		case http.MethodPut:
			s.handleUpdateChannel(w, r)
		case http.MethodDelete:
			s.handleDeleteChannel(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET, PUT, or DELETE required")
		}
	}))

	// Admin: categories
	mux.HandleFunc("/admin/categories", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.handleCreateCategory(w, r)
		case http.MethodGet:
			s.handleListCategories(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST or GET required")
		}
	}))
	mux.HandleFunc("/admin/categories/", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			s.handleUpdateCategory(w, r)
		case http.MethodDelete:
			s.handleDeleteCategory(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT or DELETE required")
		}
	}))

	// Admin: EPG sources
	mux.HandleFunc("/admin/epg-sources", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleListEpgSources(w, r)
		case http.MethodPost:
			s.handleCreateEpgSource(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		}
	}))
	mux.HandleFunc("/admin/epg-sources/", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// [admin, epg-sources, {id}] or [admin, epg-sources, {id}, sync]
		if len(segments) == 4 && segments[3] == "sync" {
			s.handleTriggerEpgSync(w, r)
			return
		}
		switch r.Method {
		case http.MethodPut:
			s.handleUpdateEpgSource(w, r)
		case http.MethodDelete:
			s.handleDeleteEpgSource(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT or DELETE required")
		}
	}))

	// Admin: featured lists
	mux.HandleFunc("/admin/featured-lists", requireSuperowner(db, s.handleListFeaturedLists))
	mux.HandleFunc("/admin/featured-lists/", requireSuperowner(db, func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// [admin, featured-lists, {slug}] => PATCH
		// [admin, featured-lists, {slug}, channels] => GET / PUT
		// [admin, featured-lists, {slug}, channels, {channel_id}] => DELETE
		switch len(segments) {
		case 3:
			if r.Method == http.MethodPatch {
				s.handlePatchFeaturedList(w, r)
			} else {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
			}
		case 4:
			if segments[3] != "channels" {
				writeError(w, http.StatusNotFound, "not_found", "Not found"); return
			}
			switch r.Method {
			case http.MethodGet:
				s.handleGetFeaturedListChannels(w, r)
			case http.MethodPut:
				s.handleReplaceFeaturedListChannels(w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT required")
			}
		case 5:
			if segments[3] != "channels" {
				writeError(w, http.StatusNotFound, "not_found", "Not found"); return
			}
			if r.Method == http.MethodDelete {
				s.handleRemoveFeaturedEntry(w, r)
			} else {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			}
		default:
			writeError(w, http.StatusNotFound, "not_found", "Not found")
		}
	}))

	log.Printf("[catalog] starting on :%s, logos at %s", port, logoDir)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("[catalog] server error: %v", err)
	}
}
