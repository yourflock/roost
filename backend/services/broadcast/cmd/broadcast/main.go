// main.go — Roost Broadcast Studio Service.
// Manages live broadcast session lifecycle: stream key issuance, HLS manifest
// tracking, viewer counts, and session state transitions (idle → live → ended).
// RTMP ingest and HLS segment writing are handled by an FFmpeg sidecar process
// that watches the stream key; this service manages metadata only.
//
// Port: 8111 (env: BROADCAST_PORT). Internal service — called by flock backend.
//
// Routes:
//   POST /broadcast/sessions              — create session, get stream key
//   GET  /broadcast/sessions              — list family sessions
//   GET  /broadcast/sessions/{id}         — get session details
//   POST /broadcast/sessions/{id}/start   — mark session live
//   POST /broadcast/sessions/{id}/end     — mark session ended
//   GET  /broadcast/sessions/{id}/hls     — redirect to HLS manifest URL
//   PUT  /broadcast/sessions/{id}/viewers — increment/decrement viewer count
//   GET  /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

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
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db, db.PingContext(ctx)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func requireFamilyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Family-ID") == "" || r.Header.Get("X-User-ID") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "X-Family-ID and X-User-ID headers required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── models ──────────────────────────────────────────────────────────────────

type Session struct {
	ID             string  `json:"id"`
	FamilyID       string  `json:"family_id"`
	StreamKey      string  `json:"stream_key"`
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	HLSManifestKey string  `json:"hls_manifest_key"`
	ViewerCount    int     `json:"viewer_count"`
	StartedAt      *string `json:"started_at"`
	EndedAt        *string `json:"ended_at"`
	CreatedAt      string  `json:"created_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── handlers ────────────────────────────────────────────────────────────────

// handleCreate creates a new broadcast session and returns a unique stream key.
// The caller uses this stream key with their RTMP encoder (e.g., OBS).
func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		body.Title = "Live Stream"
	}

	sessionID := uuid.New().String()
	streamKey := uuid.New().String()
	manifestKey := fmt.Sprintf("broadcasts/%s/%s/index.m3u8", familyID, sessionID)

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO broadcast_sessions (family_id, stream_key, title, hls_manifest_key)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		familyID, streamKey, body.Title, manifestKey,
	).Scan(&id)
	if err != nil {
		log.Printf("[broadcast] create error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create session")
		return
	}

	r2Endpoint := getEnv("R2_ENDPOINT", "https://r2.yourflock.org")
	r2Bucket := getEnv("R2_STREAM_BUCKET", "roost-streams")

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":               id,
		"stream_key":       streamKey,
		"hls_manifest_key": manifestKey,
		"hls_url":          fmt.Sprintf("%s/%s/%s", r2Endpoint, r2Bucket, manifestKey),
		"status":           "idle",
	})
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, family_id, stream_key, title, status,
		        COALESCE(hls_manifest_key, ''), viewer_count,
		        created_at::text
		 FROM broadcast_sessions
		 WHERE family_id = $1
		 ORDER BY created_at DESC LIMIT 50`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var sess Session
		if err := rows.Scan(
			&sess.ID, &sess.FamilyID, &sess.StreamKey, &sess.Title,
			&sess.Status, &sess.HLSManifestKey, &sess.ViewerCount, &sess.CreatedAt,
		); err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var sess Session
	var startedAt, endedAt sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, family_id, stream_key, title, status,
		        COALESCE(hls_manifest_key, ''), viewer_count,
		        started_at::text, ended_at::text, created_at::text
		 FROM broadcast_sessions WHERE id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(
		&sess.ID, &sess.FamilyID, &sess.StreamKey, &sess.Title,
		&sess.Status, &sess.HLSManifestKey, &sess.ViewerCount,
		&startedAt, &endedAt, &sess.CreatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if startedAt.Valid {
		sess.StartedAt = &startedAt.String
	}
	if endedAt.Valid {
		sess.EndedAt = &endedAt.String
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE broadcast_sessions SET status = 'live', started_at = $1
		 WHERE id = $2 AND family_id = $3 AND status = 'idle'`,
		now, id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusConflict, "invalid_state", "session not found or not in idle state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "live", "started_at": now})
}

func (s *server) handleEnd(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE broadcast_sessions SET status = 'ended', ended_at = $1
		 WHERE id = $2 AND family_id = $3 AND status = 'live'`,
		now, id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusConflict, "invalid_state", "session not found or not live")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ended", "ended_at": now})
}

// handleHLS redirects to the HLS manifest on R2.
func (s *server) handleHLS(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var manifestKey sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT hls_manifest_key FROM broadcast_sessions WHERE id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(&manifestKey)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil || !manifestKey.Valid || manifestKey.String == "" {
		writeError(w, http.StatusNotFound, "no_manifest", "no HLS manifest available")
		return
	}

	r2Endpoint := getEnv("R2_ENDPOINT", "https://r2.yourflock.org")
	r2Bucket := getEnv("R2_STREAM_BUCKET", "roost-streams")
	http.Redirect(w, r,
		fmt.Sprintf("%s/%s/%s", r2Endpoint, r2Bucket, manifestKey.String),
		http.StatusTemporaryRedirect,
	)
}

// handleViewers increments or decrements the viewer_count atomically.
// Body: {"delta": 1} or {"delta": -1}
func (s *server) handleViewers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Delta int `json:"delta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Delta == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "delta must be non-zero")
		return
	}

	var count int
	err := s.db.QueryRowContext(r.Context(),
		`UPDATE broadcast_sessions
		 SET viewer_count = GREATEST(0, viewer_count + $1)
		 WHERE id = $2 AND status = 'live'
		 RETURNING viewer_count`,
		body.Delta, id,
	).Scan(&count)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "live session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"viewer_count": count})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-broadcast"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[broadcast] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", srv.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(requireFamilyAuth)
		r.Post("/broadcast/sessions", srv.handleCreate)
		r.Get("/broadcast/sessions", srv.handleList)
		r.Get("/broadcast/sessions/{id}", srv.handleGet)
		r.Post("/broadcast/sessions/{id}/start", srv.handleStart)
		r.Post("/broadcast/sessions/{id}/end", srv.handleEnd)
		r.Get("/broadcast/sessions/{id}/hls", srv.handleHLS)
		r.Put("/broadcast/sessions/{id}/viewers", srv.handleViewers)
	})

	port := getEnv("BROADCAST_PORT", "8111")
	addr := ":" + port
	log.Printf("[broadcast] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[broadcast] server error: %v", err)
	}
}
