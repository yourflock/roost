// main.go — Roost VOD Service.
// Manages video-on-demand content: movies and series with season/episode hierarchy.
// Handles artwork uploads, bulk imports, and watch progress (resume playback).
// Port: 8097 (env: VOD_PORT). Internal service proxied via Nginx.
//
// Admin routes (require superowner JWT):
//   POST   /admin/vod/movies                                — create movie
//   GET    /admin/vod/movies                                — list movies (paginated, filtered)
//   GET    /admin/vod/movies/:id                            — get movie
//   PUT    /admin/vod/movies/:id                            — update movie
//   DELETE /admin/vod/movies/:id                            — soft delete (is_active=false)
//   POST   /admin/vod/series                                — create series
//   GET    /admin/vod/series                                — list series
//   GET    /admin/vod/series/:id                            — get series with seasons+episodes
//   PUT    /admin/vod/series/:id                            — update series
//   DELETE /admin/vod/series/:id                            — soft delete
//   POST   /admin/vod/series/:id/seasons                    — add season
//   POST   /admin/vod/series/:id/seasons/:sid/episodes      — add episode
//   PUT    /admin/vod/episodes/:eid                         — update episode
//   DELETE /admin/vod/episodes/:eid                         — delete episode
//   POST   /admin/vod/:id/poster                            — upload poster (multipart, ≤2MB)
//   POST   /admin/vod/:id/backdrop                          — upload backdrop (multipart, ≤5MB)
//   POST   /admin/vod/import                                — bulk import JSON array
//
// Subscriber routes (require Owl session token via X-Session-Token or Authorization):
//   GET  /vod/catalog                                       — browse catalog (paginated, filtered)
//   GET  /vod/catalog/:id                                   — get content details + episode list
//   GET  /stream/vod/:id/stream.m3u8                        — signed HLS stream redirect
//   GET  /vod/progress/:type/:id                            — get watch progress
//   PUT  /vod/progress/:type/:id                            — upsert watch progress
//   GET  /vod/continue-watching                             — incomplete items ordered by recency
//
// Artwork:
//   GET  /artwork/:filename                                 — serve uploaded artwork
//
// Health:
//   GET  /health
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	_ "github.com/lib/pq"
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

func pathSegment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n >= len(parts) {
		return ""
	}
	return parts[n]
}

// ---- auth -------------------------------------------------------------------

// validateAdminToken checks the Authorization: Bearer header for a superowner JWT.
// Delegates to the shared internal/auth package pattern (DB lookup).
func validateAdminToken(ctx context.Context, db *sql.DB, r *http.Request) (bool, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false, nil
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	var isSuperowner bool
	err := db.QueryRowContext(ctx,
		`SELECT is_superowner FROM subscribers WHERE api_token = $1 AND is_superowner = true`,
		token).Scan(&isSuperowner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return isSuperowner, err
}

// validateSessionToken looks up an owl_sessions row.
func validateSessionToken(ctx context.Context, db *sql.DB, r *http.Request) (string, error) {
	tok := r.Header.Get("X-Session-Token")
	if tok == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			tok = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if tok == "" {
		return "", nil
	}
	var subID string
	err := db.QueryRowContext(ctx,
		`SELECT subscriber_id FROM owl_sessions WHERE session_token = $1 AND expires_at > NOW()`,
		tok).Scan(&subID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return subID, err
}

// ---- signed stream URL ------------------------------------------------------

func signedStreamURL(vodID string) (string, time.Time) {
	secret := getEnv("STREAM_HMAC_SECRET", "dev-secret-change-in-production")
	baseURL := getEnv("RELAY_BASE_URL", "https://stream.roost.unity.dev")
	expiry := time.Now().UTC().Add(15 * time.Minute)
	exp := strconv.FormatInt(expiry.Unix(), 10)
	msg := vodID + ":" + exp
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	url := fmt.Sprintf("%s/stream/vod/%s/stream.m3u8?exp=%s&sig=%s", baseURL, vodID, exp, sig)
	return url, expiry
}

// ---- server -----------------------------------------------------------------

type server struct {
	db        *sql.DB
	artworkDir string
}

func newServer(db *sql.DB) *server {
	artDir := getEnv("ARTWORK_DIR", "/var/roost/artwork")
	_ = os.MkdirAll(artDir, 0o755)
	return &server{db: db, artworkDir: artDir}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", s.handleHealth)

	// Artwork serving
	mux.HandleFunc("GET /artwork/", s.handleArtwork)

	// ---- Admin routes -------------------------------------------------------
	// Movies
	mux.HandleFunc("POST /admin/vod/movies",   s.adminRequired(s.handleCreateMovie))
	mux.HandleFunc("GET /admin/vod/movies",    s.adminRequired(s.handleListMovies))
	mux.HandleFunc("GET /admin/vod/movies/",   s.adminRequired(s.handleGetMovie))
	mux.HandleFunc("PUT /admin/vod/movies/",   s.adminRequired(s.handleUpdateMovie))
	mux.HandleFunc("DELETE /admin/vod/movies/",s.adminRequired(s.handleDeleteMovie))
	// Series
	mux.HandleFunc("POST /admin/vod/series",   s.adminRequired(s.handleCreateSeries))
	mux.HandleFunc("GET /admin/vod/series",    s.adminRequired(s.handleListSeries))
	mux.HandleFunc("GET /admin/vod/series/",   s.adminRequired(s.handleGetSeries))
	mux.HandleFunc("PUT /admin/vod/series/",   s.adminRequired(s.handleUpdateSeries))
	mux.HandleFunc("DELETE /admin/vod/series/",s.adminRequired(s.handleDeleteSeries))
	// Seasons / episodes
	mux.HandleFunc("POST /admin/vod/series/{sid}/seasons",            s.adminRequired(s.handleAddSeason))
	mux.HandleFunc("POST /admin/vod/series/{sid}/seasons/{seasonid}/episodes", s.adminRequired(s.handleAddEpisode))
	mux.HandleFunc("PUT /admin/vod/episodes/",    s.adminRequired(s.handleUpdateEpisode))
	mux.HandleFunc("DELETE /admin/vod/episodes/", s.adminRequired(s.handleDeleteEpisode))
	// Artwork
	mux.HandleFunc("POST /admin/vod/{id}/poster",   s.adminRequired(s.handleUploadPoster))
	mux.HandleFunc("POST /admin/vod/{id}/backdrop", s.adminRequired(s.handleUploadBackdrop))
	// Import
	mux.HandleFunc("POST /admin/vod/import", s.adminRequired(s.handleBulkImport))

	// ---- Subscriber routes --------------------------------------------------
	mux.HandleFunc("GET /vod/catalog",  s.sessionRequired(s.handleCatalog))
	mux.HandleFunc("GET /vod/catalog/", s.sessionRequired(s.handleCatalogItem))
	mux.HandleFunc("GET /stream/vod/",  s.sessionRequired(s.handleStreamRedirect))
	mux.HandleFunc("GET /vod/progress/",  s.sessionRequired(s.handleGetProgress))
	mux.HandleFunc("PUT /vod/progress/",  s.sessionRequired(s.handleUpsertProgress))
	mux.HandleFunc("GET /vod/continue-watching", s.sessionRequired(s.handleContinueWatching))

	return mux
}

// ---- middleware -------------------------------------------------------------

func (s *server) adminRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, err := validateAdminToken(r.Context(), s.db, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Auth check failed")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Admin token required")
			return
		}
		next(w, r)
	}
}

type ctxKey string
const ctxSubscriberID ctxKey = "subscriber_id"

func (s *server) sessionRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := validateSessionToken(r.Context(), s.db, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "Session check failed")
			return
		}
		if subID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Valid session token required")
			return
		}
		ctx := context.WithValue(r.Context(), ctxSubscriberID, subID)
		next(w, r.WithContext(ctx))
	}
}

// ---- health -----------------------------------------------------------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-vod"})
}

// ---- artwork ----------------------------------------------------------------

func (s *server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/artwork/")
	if filename == "" || strings.Contains(filename, "..") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.artworkDir, filename))
}

// ---- admin: movies ----------------------------------------------------------

type vodItem struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	Slug            string      `json:"slug"`
	Type            string      `json:"type"`
	Description     *string     `json:"description,omitempty"`
	Genre           *string     `json:"genre,omitempty"`
	Rating          *string     `json:"rating,omitempty"`
	ReleaseYear     *int        `json:"release_year,omitempty"`
	DurationSeconds *int        `json:"duration_seconds,omitempty"`
	PosterURL       *string     `json:"poster_url,omitempty"`
	BackdropURL     *string     `json:"backdrop_url,omitempty"`
	TrailerURL      *string     `json:"trailer_url,omitempty"`
	IsActive        bool        `json:"is_active"`
	SortOrder       int         `json:"sort_order"`
	Metadata        interface{} `json:"metadata,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

func (s *server) handleCreateMovie(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title           string      `json:"title"`
		Slug            string      `json:"slug"`
		Description     string      `json:"description"`
		Genre           string      `json:"genre"`
		Rating          string      `json:"rating"`
		ReleaseYear     int         `json:"release_year"`
		DurationSeconds int         `json:"duration_seconds"`
		PosterURL       string      `json:"poster_url"`
		BackdropURL     string      `json:"backdrop_url"`
		TrailerURL      string      `json:"trailer_url"`
		SourceURL       string      `json:"source_url"`
		SortOrder       int         `json:"sort_order"`
		Metadata        interface{} `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.Title == "" || input.SourceURL == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "title and source_url are required")
		return
	}
	if input.Slug == "" {
		input.Slug = slugify(input.Title)
	}
	metaJSON, _ := json.Marshal(input.Metadata)
	if input.Metadata == nil {
		metaJSON = []byte("{}")
	}

	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO vod_catalog
			(title, slug, type, description, genre, rating, release_year, duration_seconds,
			 poster_url, backdrop_url, trailer_url, source_url, sort_order, metadata)
		VALUES ($1,$2,'movie',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id`,
		input.Title, input.Slug,
		nullStr(input.Description), nullStr(input.Genre), nullStr(input.Rating),
		nullInt(input.ReleaseYear), nullInt(input.DurationSeconds),
		nullStr(input.PosterURL), nullStr(input.BackdropURL), nullStr(input.TrailerURL),
		input.SourceURL, input.SortOrder, string(metaJSON),
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conflict", "Slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "slug": input.Slug})
}

func (s *server) handleListMovies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	genre := q.Get("genre")
	rating := q.Get("rating")
	yearStr := q.Get("year")
	limitStr := q.Get("limit")
	offsetStr := q.Get("offset")
	search := q.Get("q")

	limit := 50
	if l, _ := strconv.Atoi(limitStr); l > 0 && l <= 200 {
		limit = l
	}
	offset, _ := strconv.Atoi(offsetStr)

	args := []interface{}{"movie"}
	where := []string{"type = $1"}
	idx := 2

	if genre != "" {
		where = append(where, fmt.Sprintf("genre = $%d", idx))
		args = append(args, genre)
		idx++
	}
	if rating != "" {
		where = append(where, fmt.Sprintf("rating = $%d", idx))
		args = append(args, rating)
		idx++
	}
	if yearStr != "" {
		if year, err := strconv.Atoi(yearStr); err == nil {
			where = append(where, fmt.Sprintf("release_year = $%d", idx))
			args = append(args, year)
			idx++
		}
	}
	if search != "" {
		where = append(where, fmt.Sprintf("search_vector @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, search)
		idx++
	}

	whereClause := strings.Join(where, " AND ")
	args = append(args, limit, offset)
	query := fmt.Sprintf(`
		SELECT id, title, slug, type, description, genre, rating, release_year,
		       duration_seconds, poster_url, backdrop_url, trailer_url,
		       is_active, sort_order, created_at, updated_at
		FROM vod_catalog
		WHERE %s
		ORDER BY sort_order ASC, created_at DESC
		LIMIT $%d OFFSET $%d`, whereClause, idx, idx+1)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	var items []vodItem
	for rows.Next() {
		var item vodItem
		var desc, genre2, rat, poster, backdrop, trailer sql.NullString
		var yr, dur sql.NullInt64
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Slug, &item.Type,
			&desc, &genre2, &rat, &yr, &dur,
			&poster, &backdrop, &trailer,
			&item.IsActive, &item.SortOrder,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			continue
		}
		if desc.Valid { item.Description = &desc.String }
		if genre2.Valid { item.Genre = &genre2.String }
		if rat.Valid { item.Rating = &rat.String }
		if yr.Valid { v := int(yr.Int64); item.ReleaseYear = &v }
		if dur.Valid { v := int(dur.Int64); item.DurationSeconds = &v }
		if poster.Valid { item.PosterURL = &poster.String }
		if backdrop.Valid { item.BackdropURL = &backdrop.String }
		if trailer.Valid { item.TrailerURL = &trailer.String }
		items = append(items, item)
	}
	if items == nil {
		items = []vodItem{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "count": len(items), "offset": offset})
}

func (s *server) handleGetMovie(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3) // /admin/vod/movies/{id}
	s.getVODItem(w, r, id)
}

func (s *server) getVODItem(w http.ResponseWriter, r *http.Request, id string) {
	var item vodItem
	var desc, genre, rat, poster, backdrop, trailer sql.NullString
	var yr, dur sql.NullInt64
	var metaJSON []byte
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, title, slug, type, description, genre, rating, release_year,
		       duration_seconds, poster_url, backdrop_url, trailer_url,
		       is_active, sort_order, metadata, created_at, updated_at
		FROM vod_catalog WHERE id = $1`, id).Scan(
		&item.ID, &item.Title, &item.Slug, &item.Type,
		&desc, &genre, &rat, &yr, &dur,
		&poster, &backdrop, &trailer,
		&item.IsActive, &item.SortOrder, &metaJSON,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Content not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if desc.Valid { item.Description = &desc.String }
	if genre.Valid { item.Genre = &genre.String }
	if rat.Valid { item.Rating = &rat.String }
	if yr.Valid { v := int(yr.Int64); item.ReleaseYear = &v }
	if dur.Valid { v := int(dur.Int64); item.DurationSeconds = &v }
	if poster.Valid { item.PosterURL = &poster.String }
	if backdrop.Valid { item.BackdropURL = &backdrop.String }
	if trailer.Valid { item.TrailerURL = &trailer.String }
	var meta interface{}
	_ = json.Unmarshal(metaJSON, &meta)
	item.Metadata = meta
	writeJSON(w, http.StatusOK, item)
}

func (s *server) handleUpdateMovie(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	var input map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	// Build dynamic SET clause — only update provided fields
	sets := []string{}
	args := []interface{}{}
	idx := 1
	allowed := []string{"title","slug","description","genre","rating","release_year",
		"duration_seconds","poster_url","backdrop_url","trailer_url","source_url",
		"is_active","sort_order"}
	for _, f := range allowed {
		if v, ok := input[f]; ok {
			sets = append(sets, fmt.Sprintf("%s = $%d", f, idx))
			args = append(args, v)
			idx++
		}
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "No fields to update")
		return
	}
	args = append(args, id)
	_, err := s.db.ExecContext(r.Context(),
		fmt.Sprintf("UPDATE vod_catalog SET %s WHERE id = $%d AND type = 'movie'",
			strings.Join(sets, ", "), idx),
		args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	_, err := s.db.ExecContext(r.Context(),
		`UPDATE vod_catalog SET is_active = false WHERE id = $1 AND type = 'movie'`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- admin: series ----------------------------------------------------------

func (s *server) handleCreateSeries(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title       string `json:"title"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Genre       string `json:"genre"`
		Rating      string `json:"rating"`
		ReleaseYear int    `json:"release_year"`
		PosterURL   string `json:"poster_url"`
		BackdropURL string `json:"backdrop_url"`
		SourceURL   string `json:"source_url"` // series-level placeholder URL
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.Title == "" || input.SourceURL == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "title and source_url are required")
		return
	}
	if input.Slug == "" {
		input.Slug = slugify(input.Title)
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "transaction failed")
		return
	}
	defer tx.Rollback()

	var catalogID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO vod_catalog (title, slug, type, description, genre, rating, release_year,
		                         poster_url, backdrop_url, source_url)
		VALUES ($1,$2,'series',$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		input.Title, input.Slug,
		nullStr(input.Description), nullStr(input.Genre), nullStr(input.Rating),
		nullInt(input.ReleaseYear), nullStr(input.PosterURL), nullStr(input.BackdropURL),
		input.SourceURL,
	).Scan(&catalogID)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conflict", "Slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Auto-create Season 1
	var season1ID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO vod_series (catalog_id, season_number, title)
		VALUES ($1, 1, 'Season 1') RETURNING id`, catalogID).Scan(&season1ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "season 1 creation failed")
		return
	}

	_ = tx.Commit()
	writeJSON(w, http.StatusCreated, map[string]string{
		"id": catalogID, "slug": input.Slug, "season_1_id": season1ID,
	})
}

func (s *server) handleListSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if l, _ := strconv.Atoi(q.Get("limit")); l > 0 && l <= 200 {
		limit = l
	}
	offset, _ := strconv.Atoi(q.Get("offset"))

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id, c.title, c.slug, c.genre, c.rating, c.poster_url, c.is_active,
		       COUNT(DISTINCT s.id) AS seasons_count
		FROM vod_catalog c
		LEFT JOIN vod_series s ON s.catalog_id = c.id
		WHERE c.type = 'series'
		GROUP BY c.id, c.title, c.slug, c.genre, c.rating, c.poster_url, c.is_active
		ORDER BY c.sort_order ASC, c.created_at DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type seriesItem struct {
		ID           string  `json:"id"`
		Title        string  `json:"title"`
		Slug         string  `json:"slug"`
		Genre        *string `json:"genre,omitempty"`
		Rating       *string `json:"rating,omitempty"`
		PosterURL    *string `json:"poster_url,omitempty"`
		IsActive     bool    `json:"is_active"`
		SeasonsCount int     `json:"seasons_count"`
	}
	var items []seriesItem
	for rows.Next() {
		var it seriesItem
		var genre, rat, poster sql.NullString
		if err := rows.Scan(&it.ID, &it.Title, &it.Slug, &genre, &rat, &poster,
			&it.IsActive, &it.SeasonsCount); err != nil {
			continue
		}
		if genre.Valid { it.Genre = &genre.String }
		if rat.Valid { it.Rating = &rat.String }
		if poster.Valid { it.PosterURL = &poster.String }
		items = append(items, it)
	}
	if items == nil { items = []seriesItem{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "count": len(items)})
}

func (s *server) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	s.getSeriesWithSeasons(w, r, id)
}

func (s *server) getSeriesWithSeasons(w http.ResponseWriter, r *http.Request, id string) {
	// Get catalog entry
	var catalog vodItem
	var desc, genre, rat, poster, backdrop, trailer sql.NullString
	var yr sql.NullInt64
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, title, slug, type, description, genre, rating, release_year,
		       NULL, poster_url, backdrop_url, trailer_url, is_active, sort_order, created_at, updated_at
		FROM vod_catalog WHERE id = $1 AND type = 'series'`, id).Scan(
		&catalog.ID, &catalog.Title, &catalog.Slug, &catalog.Type,
		&desc, &genre, &rat, &yr,
		new(sql.NullInt64), // duration (nil for series)
		&poster, &backdrop, &trailer,
		&catalog.IsActive, &catalog.SortOrder,
		&catalog.CreatedAt, &catalog.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Series not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if desc.Valid { catalog.Description = &desc.String }
	if genre.Valid { catalog.Genre = &genre.String }
	if rat.Valid { catalog.Rating = &rat.String }
	if yr.Valid { v := int(yr.Int64); catalog.ReleaseYear = &v }
	if poster.Valid { catalog.PosterURL = &poster.String }
	if backdrop.Valid { catalog.BackdropURL = &backdrop.String }
	if trailer.Valid { catalog.TrailerURL = &trailer.String }

	// Get seasons + episodes
	type episode struct {
		ID              string    `json:"id"`
		EpisodeNumber   int       `json:"episode_number"`
		Title           string    `json:"title"`
		DurationSeconds int       `json:"duration_seconds"`
		ThumbnailURL    *string   `json:"thumbnail_url,omitempty"`
		AirDate         *string   `json:"air_date,omitempty"`
	}
	type season struct {
		ID           string    `json:"id"`
		SeasonNumber int       `json:"season_number"`
		Title        *string   `json:"title,omitempty"`
		Episodes     []episode `json:"episodes"`
	}

	seasonRows, err := s.db.QueryContext(r.Context(), `
		SELECT id, season_number, title FROM vod_series
		WHERE catalog_id = $1 ORDER BY season_number ASC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer seasonRows.Close()

	var seasons []season
	for seasonRows.Next() {
		var se season
		var title sql.NullString
		if err := seasonRows.Scan(&se.ID, &se.SeasonNumber, &title); err != nil {
			continue
		}
		if title.Valid { se.Title = &title.String }

		epRows, err := s.db.QueryContext(r.Context(), `
			SELECT id, episode_number, title, duration_seconds, thumbnail_url, air_date
			FROM vod_episodes WHERE series_id = $1 ORDER BY episode_number ASC`, se.ID)
		if err == nil {
			defer epRows.Close()
			for epRows.Next() {
				var ep episode
				var thumb sql.NullString
				var airDate sql.NullTime
				if err := epRows.Scan(&ep.ID, &ep.EpisodeNumber, &ep.Title,
					&ep.DurationSeconds, &thumb, &airDate); err != nil {
					continue
				}
				if thumb.Valid { ep.ThumbnailURL = &thumb.String }
				if airDate.Valid {
					s := airDate.Time.Format("2006-01-02")
					ep.AirDate = &s
				}
				se.Episodes = append(se.Episodes, ep)
			}
		}
		if se.Episodes == nil { se.Episodes = []episode{} }
		seasons = append(seasons, se)
	}
	if seasons == nil { seasons = []season{} }

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"catalog": catalog,
		"seasons": seasons,
	})
}

func (s *server) handleUpdateSeries(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	var input map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	sets := []string{}
	args := []interface{}{}
	idx := 1
	for _, f := range []string{"title","slug","description","genre","rating","release_year",
		"poster_url","backdrop_url","trailer_url","source_url","is_active","sort_order"} {
		if v, ok := input[f]; ok {
			sets = append(sets, fmt.Sprintf("%s = $%d", f, idx))
			args = append(args, v)
			idx++
		}
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "No fields to update")
		return
	}
	args = append(args, id)
	_, err := s.db.ExecContext(r.Context(),
		fmt.Sprintf("UPDATE vod_catalog SET %s WHERE id = $%d AND type = 'series'",
			strings.Join(sets, ", "), idx), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleDeleteSeries(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	_, err := s.db.ExecContext(r.Context(),
		`UPDATE vod_catalog SET is_active = false WHERE id = $1 AND type = 'series'`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- admin: seasons + episodes ----------------------------------------------

func (s *server) handleAddSeason(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /admin/vod/series/{sid}/seasons
	var seriesID string
	for i, p := range parts {
		if p == "series" && i+1 < len(parts) {
			seriesID = parts[i+1]
			break
		}
	}
	var input struct {
		SeasonNumber int    `json:"season_number"`
		Title        string `json:"title"`
		Description  string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.SeasonNumber <= 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "season_number must be >= 1")
		return
	}
	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO vod_series (catalog_id, season_number, title, description)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		seriesID, input.SeasonNumber,
		nullStr(input.Title), nullStr(input.Description),
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conflict", "Season number already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *server) handleAddEpisode(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /admin/vod/series/{sid}/seasons/{seasonid}/episodes
	var seasonID string
	for i, p := range parts {
		if p == "seasons" && i+1 < len(parts) {
			seasonID = parts[i+1]
			break
		}
	}
	var input struct {
		EpisodeNumber   int    `json:"episode_number"`
		Title           string `json:"title"`
		Description     string `json:"description"`
		DurationSeconds int    `json:"duration_seconds"`
		SourceURL       string `json:"source_url"`
		ThumbnailURL    string `json:"thumbnail_url"`
		AirDate         string `json:"air_date"` // YYYY-MM-DD
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.Title == "" || input.SourceURL == "" || input.DurationSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "validation_error",
			"title, source_url, and duration_seconds are required")
		return
	}
	var airDate interface{}
	if input.AirDate != "" {
		if _, err := time.Parse("2006-01-02", input.AirDate); err == nil {
			airDate = input.AirDate
		}
	}
	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO vod_episodes (series_id, episode_number, title, description,
		                          duration_seconds, source_url, thumbnail_url, air_date)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		seasonID, input.EpisodeNumber, input.Title,
		nullStr(input.Description), input.DurationSeconds, input.SourceURL,
		nullStr(input.ThumbnailURL), airDate,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conflict", "Episode number already exists in this season")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *server) handleUpdateEpisode(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	var input map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	sets := []string{}
	args := []interface{}{}
	idx := 1
	for _, f := range []string{"title","description","duration_seconds","source_url",
		"thumbnail_url","air_date","episode_number","sort_order"} {
		if v, ok := input[f]; ok {
			sets = append(sets, fmt.Sprintf("%s = $%d", f, idx))
			args = append(args, v)
			idx++
		}
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "No fields to update")
		return
	}
	args = append(args, id)
	_, err := s.db.ExecContext(r.Context(),
		fmt.Sprintf("UPDATE vod_episodes SET %s WHERE id = $%d",
			strings.Join(sets, ", "), idx), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleDeleteEpisode(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 3)
	_, err := s.db.ExecContext(r.Context(), `DELETE FROM vod_episodes WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- admin: artwork ---------------------------------------------------------

const maxPosterBytes = 2 << 20   // 2 MB
const maxBackdropBytes = 5 << 20 // 5 MB

func (s *server) handleUploadPoster(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2) // /admin/vod/{id}/poster
	s.uploadArtwork(w, r, id, "poster", maxPosterBytes)
}

func (s *server) handleUploadBackdrop(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2) // /admin/vod/{id}/backdrop
	s.uploadArtwork(w, r, id, "backdrop", maxBackdropBytes)
}

func (s *server) uploadArtwork(w http.ResponseWriter, r *http.Request, vodID, kind string, maxBytes int64) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1024)
	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeError(w, http.StatusBadRequest, "bad_request", "multipart/form-data required")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	p, err := mr.NextPart()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "No file part")
		return
	}
	defer p.Close()

	data, err := io.ReadAll(io.LimitReader(p, maxBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("File exceeds %d MB limit", maxBytes>>20))
		return
	}

	// Validate image type
	if len(data) < 4 {
		writeError(w, http.StatusBadRequest, "bad_request", "File too small")
		return
	}
	mimeType := http.DetectContentType(data)
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		writeError(w, http.StatusBadRequest, "bad_request", "Only JPEG and PNG accepted")
		return
	}

	ext := ".jpg"
	if mimeType == "image/png" {
		ext = ".png"
	}
	filename := fmt.Sprintf("%s_%s%s", vodID, kind, ext)
	dest := filepath.Join(s.artworkDir, filename)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Failed to save artwork")
		return
	}

	artURL := "/artwork/" + filename
	col := kind + "_url"
	_, err = s.db.ExecContext(r.Context(),
		fmt.Sprintf("UPDATE vod_catalog SET %s = $1 WHERE id = $2", col),
		artURL, vodID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to update URL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": artURL})
}

// ---- admin: bulk import -----------------------------------------------------

func (s *server) handleBulkImport(w http.ResponseWriter, r *http.Request) {
	var items []struct {
		Title           string      `json:"title"`
		Slug            string      `json:"slug"`
		Type            string      `json:"type"`
		Description     string      `json:"description"`
		Genre           string      `json:"genre"`
		Rating          string      `json:"rating"`
		ReleaseYear     int         `json:"release_year"`
		DurationSeconds int         `json:"duration_seconds"`
		PosterURL       string      `json:"poster_url"`
		BackdropURL     string      `json:"backdrop_url"`
		SourceURL       string      `json:"source_url"`
		Metadata        interface{} `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON array")
		return
	}

	imported := 0
	skipped := 0
	type importErr struct {
		Title string `json:"title"`
		Error string `json:"error"`
	}
	var errors []importErr

	for _, item := range items {
		if item.Title == "" || item.SourceURL == "" {
			errors = append(errors, importErr{item.Title, "title and source_url required"})
			continue
		}
		vodType := item.Type
		if vodType != "movie" && vodType != "series" {
			vodType = "movie"
		}
		slug := item.Slug
		if slug == "" {
			slug = slugify(item.Title)
		}
		metaJSON, _ := json.Marshal(item.Metadata)
		if item.Metadata == nil {
			metaJSON = []byte("{}")
		}
		_, err := s.db.ExecContext(r.Context(), `
			INSERT INTO vod_catalog
				(title, slug, type, description, genre, rating, release_year,
				 duration_seconds, poster_url, backdrop_url, source_url, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (slug) DO NOTHING`,
			item.Title, slug, vodType,
			nullStr(item.Description), nullStr(item.Genre), nullStr(item.Rating),
			nullInt(item.ReleaseYear), nullInt(item.DurationSeconds),
			nullStr(item.PosterURL), nullStr(item.BackdropURL),
			item.SourceURL, string(metaJSON),
		)
		if err != nil {
			errors = append(errors, importErr{item.Title, err.Error()})
			continue
		}
		imported++
	}
	skipped = len(items) - imported - len(errors)
	if skipped < 0 { skipped = 0 }

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	})
}

// ---- subscriber: catalog browse ---------------------------------------------

func (s *server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vodType := q.Get("type")    // "movie" or "series"
	genre := q.Get("genre")
	search := q.Get("q")
	limit := 50
	if l, _ := strconv.Atoi(q.Get("limit")); l > 0 && l <= 100 {
		limit = l
	}
	offset, _ := strconv.Atoi(q.Get("offset"))

	args := []interface{}{true}
	where := []string{"is_active = $1"}
	idx := 2

	if vodType != "" {
		where = append(where, fmt.Sprintf("type = $%d", idx))
		args = append(args, vodType)
		idx++
	}
	if genre != "" {
		where = append(where, fmt.Sprintf("genre = $%d", idx))
		args = append(args, genre)
		idx++
	}
	if search != "" {
		where = append(where, fmt.Sprintf("search_vector @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, search)
		idx++
	}

	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title, slug, type, genre, rating, release_year, duration_seconds,
		       poster_url, sort_order
		FROM vod_catalog
		WHERE %s
		ORDER BY sort_order ASC, created_at DESC
		LIMIT $%d OFFSET $%d`,
		strings.Join(where, " AND "), idx, idx+1), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type catalogEntry struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Slug            string  `json:"slug"`
		Type            string  `json:"type"`
		Genre           *string `json:"genre,omitempty"`
		Rating          *string `json:"rating,omitempty"`
		ReleaseYear     *int    `json:"release_year,omitempty"`
		DurationSeconds *int    `json:"duration_seconds,omitempty"`
		PosterURL       *string `json:"poster_url,omitempty"`
	}
	var items []catalogEntry
	for rows.Next() {
		var e catalogEntry
		var genre2, rat, poster sql.NullString
		var yr, dur sql.NullInt64
		var sortOrder int
		if err := rows.Scan(&e.ID, &e.Title, &e.Slug, &e.Type,
			&genre2, &rat, &yr, &dur, &poster, &sortOrder); err != nil {
			continue
		}
		if genre2.Valid { e.Genre = &genre2.String }
		if rat.Valid { e.Rating = &rat.String }
		if yr.Valid { v := int(yr.Int64); e.ReleaseYear = &v }
		if dur.Valid { v := int(dur.Int64); e.DurationSeconds = &v }
		if poster.Valid { e.PosterURL = &poster.String }
		items = append(items, e)
	}
	if items == nil { items = []catalogEntry{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items, "count": len(items), "offset": offset,
	})
}

func (s *server) handleCatalogItem(w http.ResponseWriter, r *http.Request) {
	// /vod/catalog/{id}
	id := pathSegment(r.URL.Path, 2)
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Content ID required")
		return
	}

	// Get basic info to determine type
	var vodType string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT type FROM vod_catalog WHERE id = $1 AND is_active = true`, id).Scan(&vodType)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Content not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	subID, _ := r.Context().Value(ctxSubscriberID).(string)

	if vodType == "series" {
		// Return series with seasons/episodes + per-episode watch progress
		s.getSeriesForSubscriber(w, r, id, subID)
		return
	}

	// Movie — return metadata + stream URL + watch progress
	var item vodItem
	var desc, genre, rat, poster, backdrop, trailer sql.NullString
	var yr, dur sql.NullInt64
	err = s.db.QueryRowContext(r.Context(), `
		SELECT id, title, slug, type, description, genre, rating, release_year,
		       duration_seconds, poster_url, backdrop_url, trailer_url,
		       is_active, sort_order, created_at, updated_at
		FROM vod_catalog WHERE id = $1`, id).Scan(
		&item.ID, &item.Title, &item.Slug, &item.Type,
		&desc, &genre, &rat, &yr, &dur,
		&poster, &backdrop, &trailer,
		&item.IsActive, &item.SortOrder,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if desc.Valid { item.Description = &desc.String }
	if genre.Valid { item.Genre = &genre.String }
	if rat.Valid { item.Rating = &rat.String }
	if yr.Valid { v := int(yr.Int64); item.ReleaseYear = &v }
	if dur.Valid { v := int(dur.Int64); item.DurationSeconds = &v }
	if poster.Valid { item.PosterURL = &poster.String }
	if backdrop.Valid { item.BackdropURL = &backdrop.String }
	if trailer.Valid { item.TrailerURL = &trailer.String }

	streamURL, expiresAt := signedStreamURL(id)

	// Watch progress
	var posSeconds int
	var completed bool
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT position_seconds, completed FROM watch_progress
		WHERE subscriber_id = $1 AND content_type = 'movie' AND content_id = $2`,
		subID, id).Scan(&posSeconds, &completed)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"content":          item,
		"stream_url":       streamURL,
		"stream_expires_at": expiresAt.Format(time.RFC3339),
		"resume_position":  posSeconds,
		"completed":        completed,
	})
}

func (s *server) getSeriesForSubscriber(w http.ResponseWriter, r *http.Request, id, subID string) {
	var catalog vodItem
	var desc, genre, rat, poster, backdrop, trailer sql.NullString
	var yr sql.NullInt64
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, title, slug, type, description, genre, rating, release_year,
		       NULL, poster_url, backdrop_url, trailer_url, is_active, sort_order, created_at, updated_at
		FROM vod_catalog WHERE id = $1`, id).Scan(
		&catalog.ID, &catalog.Title, &catalog.Slug, &catalog.Type,
		&desc, &genre, &rat, &yr,
		new(sql.NullInt64),
		&poster, &backdrop, &trailer,
		&catalog.IsActive, &catalog.SortOrder,
		&catalog.CreatedAt, &catalog.UpdatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if desc.Valid { catalog.Description = &desc.String }
	if genre.Valid { catalog.Genre = &genre.String }
	if rat.Valid { catalog.Rating = &rat.String }
	if yr.Valid { v := int(yr.Int64); catalog.ReleaseYear = &v }
	if poster.Valid { catalog.PosterURL = &poster.String }
	if backdrop.Valid { catalog.BackdropURL = &backdrop.String }
	if trailer.Valid { catalog.TrailerURL = &trailer.String }

	type epResp struct {
		ID              string  `json:"id"`
		EpisodeNumber   int     `json:"episode_number"`
		Title           string  `json:"title"`
		DurationSeconds int     `json:"duration_seconds"`
		ThumbnailURL    *string `json:"thumbnail_url,omitempty"`
		StreamURL       string  `json:"stream_url"`
		ResumePosition  int     `json:"resume_position"`
		Completed       bool    `json:"completed"`
	}
	type seasonResp struct {
		ID           string   `json:"id"`
		SeasonNumber int      `json:"season_number"`
		Title        *string  `json:"title,omitempty"`
		Episodes     []epResp `json:"episodes"`
	}

	seasonRows, err := s.db.QueryContext(r.Context(), `
		SELECT id, season_number, title FROM vod_series
		WHERE catalog_id = $1 ORDER BY season_number ASC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer seasonRows.Close()

	var seasons []seasonResp
	for seasonRows.Next() {
		var se seasonResp
		var title sql.NullString
		if err := seasonRows.Scan(&se.ID, &se.SeasonNumber, &title); err != nil {
			continue
		}
		if title.Valid { se.Title = &title.String }

		epRows, err := s.db.QueryContext(r.Context(), `
			SELECT e.id, e.episode_number, e.title, e.duration_seconds, e.thumbnail_url,
			       COALESCE(wp.position_seconds, 0), COALESCE(wp.completed, false)
			FROM vod_episodes e
			LEFT JOIN watch_progress wp ON wp.content_type = 'episode'
			    AND wp.content_id = e.id AND wp.subscriber_id = $2
			WHERE e.series_id = $1
			ORDER BY e.episode_number ASC`, se.ID, subID)
		if err == nil {
			defer epRows.Close()
			for epRows.Next() {
				var ep epResp
				var thumb sql.NullString
				if err := epRows.Scan(&ep.ID, &ep.EpisodeNumber, &ep.Title,
					&ep.DurationSeconds, &thumb, &ep.ResumePosition, &ep.Completed); err != nil {
					continue
				}
				if thumb.Valid { ep.ThumbnailURL = &thumb.String }
				ep.StreamURL, _ = signedStreamURL(ep.ID)
				se.Episodes = append(se.Episodes, ep)
			}
		}
		if se.Episodes == nil { se.Episodes = []epResp{} }
		seasons = append(seasons, se)
	}
	if seasons == nil { seasons = []seasonResp{} }

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"content": catalog,
		"seasons": seasons,
	})
}

// ---- subscriber: stream redirect --------------------------------------------

func (s *server) handleStreamRedirect(w http.ResponseWriter, r *http.Request) {
	// /stream/vod/{id}/stream.m3u8
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var vodID string
	for i, p := range parts {
		if p == "vod" && i+1 < len(parts) {
			vodID = parts[i+1]
			break
		}
	}
	if vodID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Content ID required")
		return
	}

	// Verify content exists and is active
	var sourceURL string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT source_url FROM vod_catalog WHERE id = $1 AND is_active = true`, vodID).Scan(&sourceURL)
	if err == sql.ErrNoRows {
		// Try episode ID
		err2 := s.db.QueryRowContext(r.Context(),
			`SELECT source_url FROM vod_episodes WHERE id = $1`, vodID).Scan(&sourceURL)
		if err2 != nil {
			writeError(w, http.StatusNotFound, "not_found", "Content not found")
			return
		}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Redirect to source URL (signed content delivery)
	// In production this would proxy through relay service
	http.Redirect(w, r, sourceURL, http.StatusTemporaryRedirect)
}

// ---- subscriber: watch progress ---------------------------------------------

func (s *server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	// /vod/progress/{type}/{id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, "bad_request", "Path: /vod/progress/{type}/{id}")
		return
	}
	contentType := parts[2]
	contentID := parts[3]
	subID, _ := r.Context().Value(ctxSubscriberID).(string)

	if contentType != "movie" && contentType != "episode" {
		writeError(w, http.StatusBadRequest, "bad_request", "type must be 'movie' or 'episode'")
		return
	}

	var pos, dur int
	var completed bool
	var lastWatched time.Time
	err := s.db.QueryRowContext(r.Context(), `
		SELECT position_seconds, duration_seconds, completed, last_watched_at
		FROM watch_progress
		WHERE subscriber_id = $1 AND content_type = $2 AND content_id = $3`,
		subID, contentType, contentID).Scan(&pos, &dur, &completed, &lastWatched)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"position_seconds": 0, "duration_seconds": 0,
			"completed": false, "progress_pct": 0,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	pct := 0
	if dur > 0 { pct = pos * 100 / dur }
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"position_seconds": pos, "duration_seconds": dur,
		"completed": completed, "progress_pct": pct,
		"last_watched_at": lastWatched.Format(time.RFC3339),
	})
}

func (s *server) handleUpsertProgress(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, "bad_request", "Path: /vod/progress/{type}/{id}")
		return
	}
	contentType := parts[2]
	contentID := parts[3]
	subID, _ := r.Context().Value(ctxSubscriberID).(string)

	if contentType != "movie" && contentType != "episode" {
		writeError(w, http.StatusBadRequest, "bad_request", "type must be 'movie' or 'episode'")
		return
	}

	var input struct {
		PositionSeconds int `json:"position_seconds"`
		DurationSeconds int `json:"duration_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.DurationSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "duration_seconds required")
		return
	}
	completed := input.PositionSeconds > 0 &&
		float64(input.PositionSeconds)/float64(input.DurationSeconds) >= 0.9

	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO watch_progress
			(subscriber_id, content_type, content_id, position_seconds, duration_seconds,
			 completed, last_watched_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (subscriber_id, content_type, content_id)
		DO UPDATE SET
			position_seconds = EXCLUDED.position_seconds,
			duration_seconds = EXCLUDED.duration_seconds,
			completed        = EXCLUDED.completed,
			last_watched_at  = NOW()`,
		subID, contentType, contentID,
		input.PositionSeconds, input.DurationSeconds, completed,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"position_seconds": input.PositionSeconds,
		"completed":        completed,
	})
}

func (s *server) handleContinueWatching(w http.ResponseWriter, r *http.Request) {
	subID, _ := r.Context().Value(ctxSubscriberID).(string)

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT wp.content_type, wp.content_id, wp.position_seconds, wp.duration_seconds,
		       wp.completed, wp.last_watched_at,
		       COALESCE(c.title, e.title) AS content_title,
		       COALESCE(c.poster_url, '') AS poster_url,
		       wp.position_seconds * 100 / GREATEST(wp.duration_seconds, 1) AS pct
		FROM watch_progress wp
		LEFT JOIN vod_catalog c ON wp.content_type = 'movie' AND c.id = wp.content_id
		LEFT JOIN vod_episodes e ON wp.content_type = 'episode' AND e.id = wp.content_id
		WHERE wp.subscriber_id = $1
		  AND wp.completed = false
		  AND (c.is_active = true OR e.id IS NOT NULL)
		ORDER BY wp.last_watched_at DESC
		LIMIT 20`, subID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type cwEntry struct {
		ContentType     string    `json:"content_type"`
		ContentID       string    `json:"content_id"`
		Title           string    `json:"title"`
		PosterURL       string    `json:"poster_url"`
		PositionSeconds int       `json:"position_seconds"`
		DurationSeconds int       `json:"duration_seconds"`
		ProgressPct     int       `json:"progress_pct"`
		LastWatchedAt   time.Time `json:"last_watched_at"`
	}
	var items []cwEntry
	for rows.Next() {
		var e cwEntry
		var title sql.NullString
		if err := rows.Scan(
			&e.ContentType, &e.ContentID,
			&e.PositionSeconds, &e.DurationSeconds,
			new(bool), &e.LastWatchedAt,
			&title, &e.PosterURL, &e.ProgressPct,
		); err != nil {
			continue
		}
		if title.Valid { e.Title = title.String }
		items = append(items, e)
	}
	if items == nil { items = []cwEntry{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

// ---- utilities --------------------------------------------------------------

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func nullStr(s string) interface{} {
	if s == "" { return nil }
	return s
}

func nullInt(i int) interface{} {
	if i == 0 { return nil }
	return i
}

// ---- main -------------------------------------------------------------------

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[vod] database connection failed: %v", err)
	}
	defer db.Close()

	srv := newServer(db)
	port := getEnv("VOD_PORT", "8097")
	addr := ":" + port

	log.Printf("[vod] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[vod] server error: %v", err)
	}
}
