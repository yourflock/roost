// main.go — Roost DVR Service.
// Subscriber-initiated cloud DVR: schedule recordings of live channels, capture
// HLS segments, assemble VOD playlists, and serve recordings via authenticated endpoints.
// Port: 8101 (env: DVR_PORT). Internal service — subscriber portal calls this via internal API.
//
// Routes:
//   POST   /dvr/recordings              — schedule new recording
//   GET    /dvr/recordings              — list subscriber's recordings
//   GET    /dvr/recordings/:id          — single recording detail
//   DELETE /dvr/recordings/:id          — delete recording (async storage cleanup)
//   GET    /dvr/quota                   — subscriber's quota usage
//   GET    /dvr/recordings/:id/play     — serve HLS playlist for playback (authenticated)
//   POST   /internal/dvr/cleanup        — admin: trigger storage cleanup for deleted recordings
//   GET    /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yourflock/roost/services/dvr/internal/scheduler"
)

// ---- config -----------------------------------------------------------------

type config struct {
	Port         string
	SegmentDir   string
	DVRScratch   string
	StorageDir   string
	PostgresURL  string
	MaxDuration  time.Duration // max recording duration (default 4h)
	PollEvery    time.Duration
}

func loadConfig() config {
	return config{
		Port:        getEnv("DVR_PORT", "8101"),
		SegmentDir:  getEnv("SEGMENT_DIR", "/var/roost/segments"),
		DVRScratch:  getEnv("DVR_SCRATCH_DIR", "/var/roost/dvr/scratch"),
		StorageDir:  getEnv("DVR_STORAGE_DIR", "/var/roost/dvr/storage"),
		PostgresURL: getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"),
		MaxDuration: 4 * time.Hour,
		PollEvery:   30 * time.Second,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---- auth middleware (minimal — trusts X-Subscriber-ID from internal auth) --

// subscriberIDFromRequest extracts the subscriber ID from the request.
// In production the relay/auth service validates JWT and passes X-Subscriber-ID.
func subscriberIDFromRequest(r *http.Request) string {
	if id := r.Header.Get("X-Subscriber-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("subscriber_id")
}

// ---- handlers ---------------------------------------------------------------

type handler struct {
	cfg  config
	db   *sql.DB
	sched *scheduler.Scheduler
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-dvr"})
}

// POST /dvr/recordings — schedule a new recording.
// Body: {"channel_id":"uuid","start_time":"RFC3339","end_time":"RFC3339","title":"..."}
// Alternatively: {"program_id":"uuid"} to auto-populate from EPG.
func (h *handler) handleSchedule(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}

	var input struct {
		ChannelID string `json:"channel_id"`
		ProgramID string `json:"program_id"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		Title     string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}

	// Resolve from EPG program if provided
	if input.ProgramID != "" && input.ChannelID == "" {
		var chanID, title string
		var start, end time.Time
		err := h.db.QueryRowContext(r.Context(), `
			SELECT channel_id, title, start_time, end_time FROM programs WHERE id=$1`,
			input.ProgramID).Scan(&chanID, &title, &start, &end)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "program not found")
			return
		}
		input.ChannelID = chanID
		if input.Title == "" {
			input.Title = title
		}
		if input.StartTime == "" {
			input.StartTime = start.Format(time.RFC3339)
		}
		if input.EndTime == "" {
			input.EndTime = end.Format(time.RFC3339)
		}
	}

	if input.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "channel_id required")
		return
	}
	if input.Title == "" {
		input.Title = "Manual Recording"
	}

	startTime, err := time.Parse(time.RFC3339, input.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid start_time (RFC3339 required)")
		return
	}
	endTime, err := time.Parse(time.RFC3339, input.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid end_time (RFC3339 required)")
		return
	}

	if startTime.Before(time.Now().Add(-5 * time.Minute)) {
		writeError(w, http.StatusBadRequest, "bad_request", "start_time must be in the future (or within 5 minutes past)")
		return
	}
	duration := endTime.Sub(startTime)
	if duration <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "end_time must be after start_time")
		return
	}
	if duration > h.cfg.MaxDuration {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("recording duration exceeds maximum of %s", h.cfg.MaxDuration))
		return
	}

	// Check quota
	quota, err := scheduler.GetQuota(r.Context(), h.db, subID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requestedHours := duration.Hours()
	if requestedHours > quota.RemainingHours {
		writeError(w, http.StatusPaymentRequired, "quota_exceeded",
			fmt.Sprintf("recording requires %.2fh but only %.2fh remaining (plan: %s)",
				requestedHours, quota.RemainingHours, quota.PlanName))
		return
	}

	// Verify channel exists
	var channelExists bool
	_ = h.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1 AND is_active=true)`,
		input.ChannelID).Scan(&channelExists)
	if !channelExists {
		writeError(w, http.StatusNotFound, "not_found", "channel not found or inactive")
		return
	}

	id := scheduler.UUIDNew()
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO dvr_recordings (id, subscriber_id, channel_id, program_id, title, start_time, end_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, subID, input.ChannelID,
		nullableString(input.ProgramID),
		input.Title, startTime, endTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         id,
		"title":      input.Title,
		"start_time": startTime.Format(time.RFC3339),
		"end_time":   endTime.Format(time.RFC3339),
		"status":     "scheduled",
		"duration_h": requestedHours,
	})
}

// GET /dvr/recordings — list subscriber's recordings.
func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}

	// Optional status filter
	statusFilter := r.URL.Query().Get("status")
	var rows *sql.Rows
	var err error
	if statusFilter != "" {
		rows, err = h.db.QueryContext(r.Context(), `
			SELECT r.id, r.channel_id, c.slug, c.name, r.title, r.start_time, r.end_time,
			       r.status, r.file_size_bytes, r.created_at
			FROM dvr_recordings r JOIN channels c ON c.id = r.channel_id
			WHERE r.subscriber_id=$1 AND r.status=$2
			ORDER BY r.start_time DESC LIMIT 100`, subID, statusFilter)
	} else {
		rows, err = h.db.QueryContext(r.Context(), `
			SELECT r.id, r.channel_id, c.slug, c.name, r.title, r.start_time, r.end_time,
			       r.status, r.file_size_bytes, r.created_at
			FROM dvr_recordings r JOIN channels c ON c.id = r.channel_id
			WHERE r.subscriber_id=$1 AND r.status != 'deleted'
			ORDER BY r.start_time DESC LIMIT 100`, subID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type item struct {
		ID           string  `json:"id"`
		ChannelID    string  `json:"channel_id"`
		ChannelSlug  string  `json:"channel_slug"`
		ChannelName  string  `json:"channel_name"`
		Title        string  `json:"title"`
		StartTime    string  `json:"start_time"`
		EndTime      string  `json:"end_time"`
		Status       string  `json:"status"`
		FileSizeMB   float64 `json:"file_size_mb"`
		CreatedAt    string  `json:"created_at"`
		DurationMins int     `json:"duration_minutes"`
	}
	var recordings []item
	for rows.Next() {
		var rec item
		var start, end, created time.Time
		var fileBytes int64
		if err := rows.Scan(&rec.ID, &rec.ChannelID, &rec.ChannelSlug, &rec.ChannelName,
			&rec.Title, &start, &end, &rec.Status, &fileBytes, &created); err != nil {
			continue
		}
		rec.StartTime = start.Format(time.RFC3339)
		rec.EndTime = end.Format(time.RFC3339)
		rec.CreatedAt = created.Format(time.RFC3339)
		rec.FileSizeMB = float64(fileBytes) / (1024 * 1024)
		rec.DurationMins = int(end.Sub(start).Minutes())
		recordings = append(recordings, rec)
	}
	if recordings == nil {
		recordings = []item{}
	}

	quota, _ := scheduler.GetQuota(r.Context(), h.db, subID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"recordings": recordings,
		"count":      len(recordings),
		"quota":      quota,
	})
}

// GET /dvr/recordings/:id — single recording detail.
func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}
	id := pathSegment(r.URL.Path, 2)
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "recording id required")
		return
	}

	var rec struct {
		ID          string
		ChannelSlug string
		Title       string
		StartTime   time.Time
		EndTime     time.Time
		Status      string
		StoragePath sql.NullString
		FileSizeB   int64
	}
	err := h.db.QueryRowContext(r.Context(), `
		SELECT r.id, c.slug, r.title, r.start_time, r.end_time, r.status, r.storage_path, r.file_size_bytes
		FROM dvr_recordings r JOIN channels c ON c.id = r.channel_id
		WHERE r.id=$1 AND r.subscriber_id=$2 AND r.status != 'deleted'`,
		id, subID).Scan(&rec.ID, &rec.ChannelSlug, &rec.Title, &rec.StartTime, &rec.EndTime,
		&rec.Status, &rec.StoragePath, &rec.FileSizeB)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "recording not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	out := map[string]interface{}{
		"id":           rec.ID,
		"channel_slug": rec.ChannelSlug,
		"title":        rec.Title,
		"start_time":   rec.StartTime.Format(time.RFC3339),
		"end_time":     rec.EndTime.Format(time.RFC3339),
		"status":       rec.Status,
		"file_size_mb": float64(rec.FileSizeB) / (1024 * 1024),
		"duration_minutes": int(rec.EndTime.Sub(rec.StartTime).Minutes()),
	}
	if rec.Status == "complete" {
		out["stream_url"] = fmt.Sprintf("/dvr/recordings/%s/play", rec.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /dvr/recordings/:id — soft-delete recording (async storage cleanup).
func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}
	id := pathSegment(r.URL.Path, 2)
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "recording id required")
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE dvr_recordings SET status='deleted', updated_at=NOW()
		WHERE id=$1 AND subscriber_id=$2 AND status IN ('complete','failed','scheduled')`,
		id, subID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "recording not found or cannot be deleted in current state")
		return
	}

	// Async cleanup of storage files
	go h.cleanupStorage(context.Background(), id, subID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// GET /dvr/quota — subscriber quota usage.
func (h *handler) handleQuota(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}
	quota, err := scheduler.GetQuota(r.Context(), h.db, subID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, quota)
}

// GET /dvr/recordings/:id/play — serve authenticated HLS playlist for playback.
func (h *handler) handlePlay(w http.ResponseWriter, r *http.Request) {
	subID := subscriberIDFromRequest(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber_id required")
		return
	}

	// Extract id from /dvr/recordings/:id/play
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}
	id := parts[2]

	var storagePath sql.NullString
	var status string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT status, storage_path FROM dvr_recordings
		WHERE id=$1 AND subscriber_id=$2`, id, subID).Scan(&status, &storagePath)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "recording not found")
		return
	}
	if status != "complete" {
		writeError(w, http.StatusConflict, "not_ready",
			fmt.Sprintf("recording is not complete (status: %s)", status))
		return
	}
	if !storagePath.Valid {
		writeError(w, http.StatusNotFound, "not_found", "recording file not available")
		return
	}

	playlistPath := storagePath.String
	if !strings.HasSuffix(playlistPath, ".m3u8") {
		playlistPath = filepath.Join(playlistPath, "recording.m3u8")
	}

	f, err := os.Open(playlistPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "recording playlist not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// POST /internal/dvr/cleanup — trigger async cleanup of deleted recordings' storage.
func (h *handler) handleCleanup(w http.ResponseWriter, r *http.Request) {
	go h.cleanupAllDeleted(context.Background())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cleanup_started"})
}

// cleanupStorage removes storage files for a single recording.
func (h *handler) cleanupStorage(ctx context.Context, recordingID, subscriberID string) {
	dir := filepath.Join(h.cfg.StorageDir, subscriberID, recordingID)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[dvr] cleanup storage %s: %v", dir, err)
	}
}

// cleanupAllDeleted removes storage files for all deleted recordings.
func (h *handler) cleanupAllDeleted(ctx context.Context) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, subscriber_id FROM dvr_recordings WHERE status='deleted' AND storage_path IS NOT NULL`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, subID string
		if err := rows.Scan(&id, &subID); err != nil {
			continue
		}
		h.cleanupStorage(ctx, id, subID)
		_, _ = h.db.ExecContext(ctx, `UPDATE dvr_recordings SET storage_path=NULL WHERE id=$1`, id)
	}
}

// ---- helpers ----------------------------------------------------------------

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

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ---- main -------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	for _, dir := range []string{cfg.DVRScratch, cfg.StorageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("[dvr] cannot create directory %s: %v", dir, err)
		}
	}

	db, err := connectDB(cfg.PostgresURL)
	if err != nil {
		log.Fatalf("[dvr] database connection failed: %v", err)
	}
	defer db.Close()

	sched := scheduler.New(scheduler.Config{
		SegmentDir: cfg.SegmentDir,
		DVRDir:     cfg.DVRScratch,
		StorageDir: cfg.StorageDir,
		PollEvery:  cfg.PollEvery,
	}, db)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	h := &handler{cfg: cfg, db: db, sched: sched}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /dvr/recordings", h.handleSchedule)
	mux.HandleFunc("GET /dvr/recordings", h.handleList)
	mux.HandleFunc("GET /dvr/quota", h.handleQuota)
	mux.HandleFunc("POST /internal/dvr/cleanup", h.handleCleanup)
	// Catch-all for /dvr/recordings/:id and /dvr/recordings/:id/play
	mux.HandleFunc("/dvr/recordings/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			h.handlePlay(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.handleGet(w, r)
		case http.MethodDelete:
			h.handleDelete(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or DELETE")
		}
	})

	addr := ":" + cfg.Port
	log.Printf("[dvr] starting on %s, storage at %s", addr, cfg.StorageDir)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	_ = strconv.Itoa // keep import
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[dvr] server error: %v", err)
	}
}

func connectDB(dsn string) (*sql.DB, error) {
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
