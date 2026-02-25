// main.go — Roost Scene Skip Service.
// Crowd-sourced scene skip sidecar API for content filtering.
// Port: 8108 (env: SKIP_PORT). Publicly accessible; write endpoints require auth.
//
// Public routes:
//   GET  /skip/v1/:content_id          — fetch .skip sidecar (approved scenes)
//   GET  /skip/v1/scenes/:id           — scene detail + vote count
//   GET  /skip/v1/stats                — contribution stats leaderboard
//
// Authenticated routes (Bearer token required):
//   POST   /skip/v1/scenes             — submit a new scene entry
//   POST   /skip/v1/scenes/:id/vote    — upvote or downvote a scene
//   DELETE /skip/v1/scenes/:id         — retract own submission
//
// Admin routes (superowner JWT):
//   GET  /skip/v1/admin/disputed       — list disputed scenes for review
//   POST /skip/v1/admin/approve/:id    — force-approve a scene
//   POST /skip/v1/admin/reject/:id     — reject + delete a scene

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	goredis "github.com/redis/go-redis/v9"

	rootauth "github.com/yourflock/roost/internal/auth"
	"github.com/yourflock/roost/internal/ratelimit"
)

// ── helpers ───────────────────────────────────────────────────────────────────

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

// validContentID checks the {source}:{id} prefix pattern.
var reContentID = regexp.MustCompile(`^[a-z]+:[a-z0-9_:.\-]+$`)

// ── server ────────────────────────────────────────────────────────────────────

type server struct {
	db      *sql.DB
	redis   *goredis.Client
	limiter *ratelimit.Limiter
}

// ── auth middleware ───────────────────────────────────────────────────────────

// requireAuth extracts and validates the Bearer token. Returns subject (subscriber UUID) on success.
func requireAuth(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("missing_token")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", fmt.Errorf("invalid_token")
	}
	claims, err := rootauth.ValidateAccessToken(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", fmt.Errorf("invalid_token")
	}
	return claims.Subject, nil
}

func (s *server) requireSuperowner(r *http.Request) (string, error) {
	userID, err := requireAuth(r)
	if err != nil {
		return "", err
	}
	var isSuperowner bool
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT is_superowner FROM subscribers WHERE id = $1`, userID).Scan(&isSuperowner)
	if !isSuperowner {
		return "", fmt.Errorf("forbidden")
	}
	return userID, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

type sceneInput struct {
	ContentID   string `json:"content_id"`
	ContentType string `json:"content_type"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Category    string `json:"category"`
	Severity    int    `json:"severity"`
	Action      string `json:"action"`
	Description string `json:"description"`
}

type sceneResponse struct {
	ID          string  `json:"id"`
	ContentID   string  `json:"content_id"`
	ContentType string  `json:"content_type"`
	Start       int     `json:"start"`
	End         int     `json:"end"`
	Category    string  `json:"category"`
	Severity    int     `json:"severity"`
	Action      string  `json:"action"`
	Description *string `json:"description,omitempty"`
	Votes       int     `json:"votes"`
	Disputed    bool    `json:"disputed"`
	Confidence  string  `json:"confidence"`
}

type sidecarResponse struct {
	ContentID   string           `json:"content_id"`
	Version     int              `json:"version"`
	Contributors int             `json:"contributors"`
	GeneratedAt string           `json:"generated_at"`
	Scenes      []sceneResponse  `json:"scenes"`
}

var validCategories = map[string]bool{
	"sex": true, "nudity": true, "kissing": true, "romance": true,
	"violence": true, "gore": true, "language": true, "drugs": true,
	"jump_scare": true, "scary": true,
}
var validActions = map[string]bool{"skip": true, "blur": true, "mute": true, "warn": true}

// ── route: GET /skip/v1/:content_id ──────────────────────────────────────────

func (s *server) handleFetchSidecar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	// Extract content_id from path: /skip/v1/{content_id}
	contentID := strings.TrimPrefix(r.URL.Path, "/skip/v1/")
	contentID = strings.TrimSuffix(contentID, "/")
	if !reContentID.MatchString(contentID) {
		writeError(w, http.StatusBadRequest, "invalid_content_id", "Content ID must match {source}:{id} format")
		return
	}

	// Try Redis cache first (1hr TTL).
	if s.redis != nil {
		cacheKey := "skip:sidecar:" + contentID
		cached, err := s.redis.Get(r.Context(), cacheKey).Result()
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Skip-Cache", "HIT")
			_, _ = fmt.Fprint(w, cached)
			return
		}
	}

	// Query approved scenes from DB.
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, content_id, content_type, start_seconds, end_seconds,
		       category, severity, action, description, vote_count, disputed
		FROM skip_scenes
		WHERE content_id = $1
		  AND approved = TRUE
		  AND disputed = FALSE
		ORDER BY start_seconds ASC
	`, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch scenes")
		return
	}
	defer rows.Close()

	var scenes []sceneResponse
	for rows.Next() {
		var sc sceneResponse
		var desc sql.NullString
		if err := rows.Scan(&sc.ID, &sc.ContentID, &sc.ContentType,
			&sc.Start, &sc.End, &sc.Category, &sc.Severity,
			&sc.Action, &desc, &sc.Votes, &sc.Disputed); err != nil {
			continue
		}
		if desc.Valid {
			sc.Description = &desc.String
		}
		sc.Confidence = "confirmed"
		scenes = append(scenes, sc)
	}
	if scenes == nil {
		scenes = []sceneResponse{}
	}

	// Count unique contributors.
	var contributors int
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(DISTINCT submitted_by) FROM skip_scenes WHERE content_id = $1`, contentID,
	).Scan(&contributors)

	resp := sidecarResponse{
		ContentID:    contentID,
		Version:      1,
		Contributors: contributors,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Scenes:       scenes,
	}

	// Cache for 1hr.
	if s.redis != nil {
		if b, err := json.Marshal(resp); err == nil {
			_ = s.redis.Set(r.Context(), "skip:sidecar:"+contentID, string(b), time.Hour).Err()
		}
	}

	w.Header().Set("X-Skip-Cache", "MISS")
	writeJSON(w, http.StatusOK, resp)
}

// ── route: POST /skip/v1/scenes ───────────────────────────────────────────────

func (s *server) handleSubmitScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	userID, err := requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error(), "Authentication required")
		return
	}

	var inp sceneInput
	if err := json.NewDecoder(r.Body).Decode(&inp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}

	// Validate.
	if !reContentID.MatchString(inp.ContentID) {
		writeError(w, http.StatusBadRequest, "invalid_content_id", "Content ID must match {source}:{id} format")
		return
	}
	if inp.End <= inp.Start || inp.Start < 0 {
		writeError(w, http.StatusBadRequest, "invalid_timestamps", "end must be greater than start; start >= 0")
		return
	}
	if !validCategories[inp.Category] {
		writeError(w, http.StatusBadRequest, "invalid_category", "Invalid category")
		return
	}
	if inp.Severity < 1 || inp.Severity > 5 {
		writeError(w, http.StatusBadRequest, "invalid_severity", "Severity must be between 1 and 5")
		return
	}
	if !validActions[inp.Action] {
		writeError(w, http.StatusBadRequest, "invalid_action", "Action must be skip, blur, mute, or warn")
		return
	}
	if inp.ContentType == "" {
		inp.ContentType = "movie"
	}
	if len(inp.Description) > 280 {
		inp.Description = inp.Description[:280]
	}

	// Insert.
	var id string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO skip_scenes
		  (content_id, content_type, start_seconds, end_seconds, category, severity, action, description, submitted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`, inp.ContentID, inp.ContentType, inp.Start, inp.End,
		inp.Category, inp.Severity, inp.Action,
		nullString(inp.Description), userID,
	).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to insert scene")
		return
	}

	// Invalidate cache for this content.
	if s.redis != nil {
		_ = s.redis.Del(r.Context(), "skip:sidecar:"+inp.ContentID).Err()
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "pending"})
}

// ── route: POST /skip/v1/scenes/:id/vote ─────────────────────────────────────

func (s *server) handleVoteScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	userID, err := requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error(), "Authentication required")
		return
	}

	// Extract scene ID from /skip/v1/scenes/{id}/vote
	sceneID := extractSceneID(r.URL.Path, "/vote")
	if _, err := uuid.Parse(sceneID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid scene UUID")
		return
	}

	var body struct {
		Vote int `json:"vote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}
	if body.Vote != 1 && body.Vote != -1 {
		writeError(w, http.StatusBadRequest, "invalid_vote", "Vote must be 1 or -1")
		return
	}

	// Upsert vote.
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO skip_votes (scene_id, user_id, vote)
		VALUES ($1, $2, $3)
		ON CONFLICT (scene_id, user_id) DO UPDATE SET vote = $3, voted_at = NOW()
	`, sceneID, userID, body.Vote)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to record vote")
		return
	}

	// Invalidate cache.
	if s.redis != nil {
		var contentID string
		_ = s.db.QueryRowContext(r.Context(),
			`SELECT content_id FROM skip_scenes WHERE id = $1`, sceneID).Scan(&contentID)
		if contentID != "" {
			_ = s.redis.Del(r.Context(), "skip:sidecar:"+contentID).Err()
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── route: GET /skip/v1/scenes/:id ───────────────────────────────────────────

func (s *server) handleGetScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	sceneID := extractSceneID(r.URL.Path, "")
	if _, err := uuid.Parse(sceneID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid scene UUID")
		return
	}

	var sc sceneResponse
	var desc sql.NullString
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, content_id, content_type, start_seconds, end_seconds,
		       category, severity, action, description, vote_count, disputed, approved
		FROM skip_scenes WHERE id = $1
	`, sceneID).Scan(&sc.ID, &sc.ContentID, &sc.ContentType,
		&sc.Start, &sc.End, &sc.Category, &sc.Severity,
		&sc.Action, &desc, &sc.Votes, &sc.Disputed, new(bool))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Scene not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch scene")
		return
	}
	if desc.Valid {
		sc.Description = &desc.String
	}
	if sc.Votes >= 5 {
		sc.Confidence = "confirmed"
	} else {
		sc.Confidence = "community_estimate"
	}
	writeJSON(w, http.StatusOK, sc)
}

// ── route: DELETE /skip/v1/scenes/:id ────────────────────────────────────────

func (s *server) handleDeleteScene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE only")
		return
	}
	userID, err := requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error(), "Authentication required")
		return
	}
	sceneID := extractSceneID(r.URL.Path, "")
	if _, err := uuid.Parse(sceneID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid scene UUID")
		return
	}

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM skip_scenes WHERE id = $1 AND submitted_by = $2`, sceneID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Delete failed")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Scene not found or not owned by you")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── route: GET /skip/v1/stats ─────────────────────────────────────────────────

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	type leaderEntry struct {
		UserID        string `json:"user_id"`
		SceneCount    int    `json:"scene_count"`
		ApprovedCount int    `json:"approved_count"`
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT submitted_by,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE approved = TRUE) AS approved
		FROM skip_scenes
		GROUP BY submitted_by
		ORDER BY approved DESC, total DESC
		LIMIT 20
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch stats")
		return
	}
	defer rows.Close()

	var entries []leaderEntry
	for rows.Next() {
		var e leaderEntry
		if err := rows.Scan(&e.UserID, &e.SceneCount, &e.ApprovedCount); err == nil {
			entries = append(entries, e)
		}
	}
	if entries == nil {
		entries = []leaderEntry{}
	}

	var totalScenes, totalApproved int
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE approved=TRUE) FROM skip_scenes`,
	).Scan(&totalScenes, &totalApproved)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_scenes":   totalScenes,
		"approved_scenes": totalApproved,
		"top_contributors": entries,
	})
}

// ── admin routes ──────────────────────────────────────────────────────────────

func (s *server) handleAdminDisputed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if _, err := s.requireSuperowner(r); err != nil {
		if err.Error() == "forbidden" {
			writeError(w, http.StatusForbidden, "forbidden", "Superowner access required")
		} else {
			writeError(w, http.StatusUnauthorized, err.Error(), "Authentication required")
		}
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, content_id, content_type, start_seconds, end_seconds,
		       category, severity, action, description, vote_count, disputed
		FROM skip_scenes WHERE disputed = TRUE
		ORDER BY submitted_at DESC LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to fetch")
		return
	}
	defer rows.Close()

	var scenes []sceneResponse
	for rows.Next() {
		var sc sceneResponse
		var desc sql.NullString
		if err := rows.Scan(&sc.ID, &sc.ContentID, &sc.ContentType,
			&sc.Start, &sc.End, &sc.Category, &sc.Severity,
			&sc.Action, &desc, &sc.Votes, &sc.Disputed); err == nil {
			if desc.Valid {
				sc.Description = &desc.String
			}
			sc.Confidence = "community_estimate"
			scenes = append(scenes, sc)
		}
	}
	if scenes == nil {
		scenes = []sceneResponse{}
	}
	writeJSON(w, http.StatusOK, scenes)
}

func (s *server) handleAdminApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if _, err := s.requireSuperowner(r); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Superowner access required")
		return
	}
	sceneID := strings.TrimPrefix(r.URL.Path, "/skip/v1/admin/approve/")
	_, err := s.db.ExecContext(r.Context(),
		`UPDATE skip_scenes SET approved = TRUE, disputed = FALSE WHERE id = $1`, sceneID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to approve")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *server) handleAdminReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if _, err := s.requireSuperowner(r); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Superowner access required")
		return
	}
	sceneID := strings.TrimPrefix(r.URL.Path, "/skip/v1/admin/reject/")
	_, err := s.db.ExecContext(r.Context(),
		`DELETE FROM skip_scenes WHERE id = $1`, sceneID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to reject")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// ── routing helpers ───────────────────────────────────────────────────────────

// extractSceneID parses /{...}/scenes/{uuid}/{suffix} and returns the UUID.
func extractSceneID(path, suffix string) string {
	p := strings.TrimSuffix(path, suffix)
	p = strings.TrimSuffix(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	port := getEnv("SKIP_PORT", "8108")

	db, err := connectDB()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Printf("Database connected")

	var rdb *goredis.Client
	if redisURL := getEnv("REDIS_URL", ""); redisURL != "" {
		rdb = goredis.NewClient(&goredis.Options{Addr: redisURL})
		log.Printf("Redis connected: %s", redisURL)
	}

	var redisStore ratelimit.Store
	if rdb != nil {
		redisStore = ratelimit.NewRedisStore(rdb)
	}
	limiter := ratelimit.New(redisStore)
	_ = limiter // applied per-route in future

	srv := &server{db: db, redis: rdb, limiter: limiter}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "skip"})
	})

	// Public sidecar fetch: /skip/v1/{content_id}
	// Must handle before /skip/v1/scenes since both share /skip/v1/ prefix.
	mux.HandleFunc("/skip/v1/stats", srv.handleStats)
	mux.HandleFunc("/skip/v1/admin/disputed", srv.handleAdminDisputed)
	mux.HandleFunc("/skip/v1/admin/approve/", srv.handleAdminApprove)
	mux.HandleFunc("/skip/v1/admin/reject/", srv.handleAdminReject)

	// Scene CRUD.
	mux.HandleFunc("/skip/v1/scenes", srv.dispatchScenes) // POST
	mux.HandleFunc("/skip/v1/scenes/", srv.dispatchScene) // GET / DELETE / POST vote

	// Sidecar fetch — catch-all under /skip/v1/ (must be last).
	mux.HandleFunc("/skip/v1/", srv.handleFetchSidecar)

	addr := ":" + port
	log.Printf("Skip service listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// dispatchScenes routes POST /skip/v1/scenes.
func (s *server) dispatchScenes(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleSubmitScene(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
}

// dispatchScene routes GET|DELETE /skip/v1/scenes/:id and POST /skip/v1/scenes/:id/vote.
func (s *server) dispatchScene(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/vote") {
		s.handleVoteScene(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGetScene(w, r)
	case http.MethodDelete:
		s.handleDeleteScene(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET, DELETE, or POST /vote")
	}
}
