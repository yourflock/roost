// main.go — Roost Clip Economy Service.
// Slices DVR segments into shareable clips using FFmpeg, stores clip metadata in
// Postgres, and uploads the clip file to R2. Clips can be shared across family
// members or externally via a signed URL.
//
// Port: 8113 (env: CLIPS_PORT). Internal service.
//
// Routes:
//   POST /clips                       — create clip from segment (triggers FFmpeg)
//   GET  /clips                       — list family clips
//   GET  /clips/{id}                  — get clip details
//   DELETE /clips/{id}                — soft-delete clip
//   POST /clips/{id}/share            — increment share counter, return signed URL
//   GET  /clips/{id}/thumbnail        — redirect to thumbnail on R2
//   GET  /health
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// ─── R2 helpers ──────────────────────────────────────────────────────────────

func uploadToR2(r2Key string, data []byte, contentType string) error {
	endpoint := getEnv("R2_ENDPOINT", "https://r2.roost.unity.dev")
	bucket := getEnv("R2_CLIPS_BUCKET", "roost-vod")
	accessKey := getEnv("R2_ACCESS_KEY_ID", "")
	secretKey := getEnv("R2_SECRET_ACCESS_KEY", "")

	url := fmt.Sprintf("%s/%s/%s", endpoint, bucket, r2Key)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("r2 build request: %w", err)
	}
	date := time.Now().UTC().Format("20060102")
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(fmt.Sprintf("%s:%s:%s", accessKey, date, r2Key)))
	sig := hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", fmt.Sprintf("ROOST-HMAC %s:%s", accessKey, sig))
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("r2 do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("r2 status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// signedURL returns an HMAC-signed URL valid for 4 hours.
func signedURL(r2Key string) string {
	secretKey := getEnv("R2_SECRET_ACCESS_KEY", "change-me")
	endpoint := getEnv("R2_ENDPOINT", "https://r2.roost.unity.dev")
	bucket := getEnv("R2_CLIPS_BUCKET", "roost-vod")
	expiry := time.Now().Add(4 * time.Hour).Unix()
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(fmt.Sprintf("%s:%d", r2Key, expiry)))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s/%s/%s?expires=%d&sig=%s", endpoint, bucket, r2Key, expiry, sig)
}

// ─── FFmpeg ───────────────────────────────────────────────────────────────────

// sliceClip uses FFmpeg to cut a clip from a source segment (HLS/TS file or R2 URL).
// Returns the output MP4 file path and duration in seconds.
func sliceClip(segmentURL, outDir, clipID string, startSec, durationSec float64) (string, int, error) {
	outPath := filepath.Join(outDir, clipID+".mp4")
	// ffmpeg -ss {start} -i {input} -t {duration} -c copy -movflags +faststart {output}
	args := []string{
		"-y",
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-i", segmentURL,
		"-t", fmt.Sprintf("%.3f", durationSec),
		"-c", "copy",
		"-movflags", "+faststart",
		outPath,
	}
	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("ffmpeg error: %v — %s", err, stderr.String())
	}
	return outPath, int(durationSec), nil
}

// extractThumbnail uses FFmpeg to grab the first frame of a clip as JPEG.
func extractThumbnail(clipPath, outDir, clipID string) (string, error) {
	thumbPath := filepath.Join(outDir, clipID+"_thumb.jpg")
	args := []string{
		"-y",
		"-i", clipPath,
		"-vframes", "1",
		"-q:v", "3",
		thumbPath,
	}
	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg thumbnail error: %v", err)
	}
	return thumbPath, nil
}

// ─── models ──────────────────────────────────────────────────────────────────

type Clip struct {
	ID               string  `json:"id"`
	FamilyID         string  `json:"family_id"`
	SourceSegmentKey string  `json:"source_segment_key"`
	Title            string  `json:"title"`
	DurationSecs     int     `json:"duration_secs"`
	ThumbnailKey     string  `json:"thumbnail_key,omitempty"`
	ShareCount       int     `json:"share_count"`
	CreatedAt        string  `json:"created_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── handlers ────────────────────────────────────────────────────────────────

// handleCreate slices a clip from a DVR segment asynchronously and records it in DB.
func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		SegmentURL  string  `json:"segment_url"`
		Title       string  `json:"title"`
		StartSec    float64 `json:"start_sec"`
		DurationSec float64 `json:"duration_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.SegmentURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "segment_url is required")
		return
	}
	if body.DurationSec <= 0 || body.DurationSec > 300 {
		writeError(w, http.StatusBadRequest, "bad_request", "duration_sec must be between 1 and 300")
		return
	}
	if body.Title == "" {
		body.Title = fmt.Sprintf("Clip %s", time.Now().Format("Jan 2 15:04"))
	}

	clipID := uuid.New().String()
	r2ClipKey := fmt.Sprintf("clips/%s/%s.mp4", familyID, clipID)
	r2ThumbKey := fmt.Sprintf("clips/%s/%s_thumb.jpg", familyID, clipID)

	// Insert record immediately with status info; async goroutine does the actual encode.
	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO clips (family_id, source_segment_key, title, duration_secs)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		familyID, body.SegmentURL, body.Title, int(body.DurationSec),
	).Scan(&id)
	if err != nil {
		log.Printf("[clips] db insert error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create clip record")
		return
	}

	// Async clip encoding.
	go func() {
		log.Printf("[clips] encoding clip %s for family %s", id, familyID)
		tmpDir := os.TempDir()

		clipPath, actualDur, err := sliceClip(body.SegmentURL, tmpDir, clipID, body.StartSec, body.DurationSec)
		if err != nil {
			log.Printf("[clips] ffmpeg slice error for %s: %v", id, err)
			return
		}
		defer os.Remove(clipPath)

		clipData, err := os.ReadFile(clipPath)
		if err != nil {
			log.Printf("[clips] read clip file error: %v", err)
			return
		}
		if err := uploadToR2(r2ClipKey, clipData, "video/mp4"); err != nil {
			log.Printf("[clips] r2 upload error for %s: %v", id, err)
			return
		}

		thumbPath, thumbErr := extractThumbnail(clipPath, tmpDir, clipID)
		thumbKey := ""
		if thumbErr == nil {
			defer os.Remove(thumbPath)
			thumbData, err := os.ReadFile(thumbPath)
			if err == nil {
				if err := uploadToR2(r2ThumbKey, thumbData, "image/jpeg"); err == nil {
					thumbKey = r2ThumbKey
				}
			}
		}

		ctx := context.Background()
		if thumbKey != "" {
			s.db.ExecContext(ctx,
				`UPDATE clips SET duration_secs = $1, thumbnail_key = $2 WHERE id = $3`,
				actualDur, thumbKey, id,
			)
		} else {
			s.db.ExecContext(ctx,
				`UPDATE clips SET duration_secs = $1 WHERE id = $2`,
				actualDur, id,
			)
		}
		log.Printf("[clips] clip %s encoded and uploaded (dur=%ds)", id, actualDur)
	}()

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":       id,
		"r2_key":   r2ClipKey,
		"status":   "encoding",
	})
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, family_id, source_segment_key, title, duration_secs,
		        COALESCE(thumbnail_key, ''), share_count, created_at::text
		 FROM clips
		 WHERE family_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 100`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	clips := []Clip{}
	for rows.Next() {
		var c Clip
		if err := rows.Scan(&c.ID, &c.FamilyID, &c.SourceSegmentKey, &c.Title,
			&c.DurationSecs, &c.ThumbnailKey, &c.ShareCount, &c.CreatedAt); err != nil {
			continue
		}
		clips = append(clips, c)
	}
	writeJSON(w, http.StatusOK, clips)
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var c Clip
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, family_id, source_segment_key, title, duration_secs,
		        COALESCE(thumbnail_key, ''), share_count, created_at::text
		 FROM clips WHERE id = $1 AND family_id = $2 AND deleted_at IS NULL`,
		id, familyID,
	).Scan(&c.ID, &c.FamilyID, &c.SourceSegmentKey, &c.Title,
		&c.DurationSecs, &c.ThumbnailKey, &c.ShareCount, &c.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "clip not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE clips SET deleted_at = now() WHERE id = $1 AND family_id = $2 AND deleted_at IS NULL`,
		id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "clip not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleShare increments the share counter and returns a signed 4-hour URL.
func (s *server) handleShare(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var segmentKey string
	err := s.db.QueryRowContext(r.Context(),
		`UPDATE clips SET share_count = share_count + 1
		 WHERE id = $1 AND family_id = $2 AND deleted_at IS NULL
		 RETURNING source_segment_key`,
		id, familyID,
	).Scan(&segmentKey)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "clip not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	r2ClipKey := fmt.Sprintf("clips/%s/%s.mp4", familyID, id)
	url := signedURL(r2ClipKey)
	writeJSON(w, http.StatusOK, map[string]string{"url": url, "expires_in": "4h"})
}

// handleThumbnail redirects to the thumbnail on R2.
func (s *server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var thumbKey sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT thumbnail_key FROM clips WHERE id = $1 AND family_id = $2 AND deleted_at IS NULL`,
		id, familyID,
	).Scan(&thumbKey)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "clip not found")
		return
	}
	if !thumbKey.Valid || thumbKey.String == "" {
		writeError(w, http.StatusNotFound, "no_thumbnail", "thumbnail not yet available")
		return
	}

	endpoint := getEnv("R2_ENDPOINT", "https://r2.roost.unity.dev")
	bucket := getEnv("R2_CLIPS_BUCKET", "roost-vod")
	http.Redirect(w, r, fmt.Sprintf("%s/%s/%s", endpoint, bucket, thumbKey.String), http.StatusTemporaryRedirect)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-clips"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[clips] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second)) // clip creation can take time

	r.Get("/health", srv.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(requireFamilyAuth)
		r.Post("/clips", srv.handleCreate)
		r.Get("/clips", srv.handleList)
		r.Get("/clips/{id}", srv.handleGet)
		r.Delete("/clips/{id}", srv.handleDelete)
		r.Post("/clips/{id}/share", srv.handleShare)
		r.Get("/clips/{id}/thumbnail", srv.handleThumbnail)
	})

	port := getEnv("CLIPS_PORT", "8113")
	addr := ":" + port
	log.Printf("[clips] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[clips] server error: %v", err)
	}
}
