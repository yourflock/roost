// ingest.go — VOD content ingest pipeline for Roost.
//
// Pipeline:
//  1. Receive ingest request (URL, type, title) → create IngestJob record
//  2. Download source file to temp directory
//  3. Probe with ffprobe: get codec, resolution, duration, bitrate
//  4. If not already HLS-compatible: transcode with ffmpeg → HLS segments
//  5. Upload HLS segments + playlist to R2
//  6. Create or update catalog entry in the vod DB table
//  7. Update job status to "done"
//
// Job status values: queued → downloading → probing → transcoding → uploading → done | failed
//
// Env vars required:
//
//	R2_ENDPOINT    — Cloudflare R2 S3-compatible endpoint
//	R2_ACCESS_KEY  — R2 access key ID
//	R2_SECRET_KEY  — R2 secret access key
//	R2_VOD_BUCKET  — R2 bucket name for VOD content (default: roost-vod)
//	VOD_TEMP_DIR   — temp directory for downloads (default: /tmp/roost-vod)
//	FFMPEG_PATH    — path to ffmpeg binary (default: ffmpeg)
//	FFPROBE_PATH   — path to ffprobe binary (default: ffprobe)
package vod

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// IngestJob tracks the state of a VOD ingest operation.
type IngestJob struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Type        string `json:"type"`  // movie | show | music | podcast | game
	Title       string `json:"title"`
	Status      string `json:"status"` // queued | downloading | probing | transcoding | uploading | done | failed
	Error       string `json:"error,omitempty"`
	R2Key       string `json:"r2_key,omitempty"`   // R2 HLS playlist key when done
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// jobStore is an in-process job registry (DB-backed in production; in-memory for single-node).
// A real deployment should persist jobs to the ingest_jobs DB table.
var (
	jobsMu sync.RWMutex
	jobs   = make(map[string]*IngestJob)
)

// JobStore wraps DB access for ingest job persistence.
type JobStore struct {
	DB *sql.DB
}

// NewJobStore creates a JobStore backed by db.
func NewJobStore(db *sql.DB) *JobStore {
	return &JobStore{DB: db}
}

// StartIngest creates a new IngestJob and starts the pipeline in a goroutine.
// Returns the job ID immediately. Poll GetJob to track progress.
func StartIngest(ctx context.Context, store *JobStore, url, contentType, title string) (*IngestJob, error) {
	if url == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("url must be http or https")
	}
	if contentType == "" {
		contentType = "movie"
	}
	if title == "" {
		title = filepath.Base(url)
	}

	job := &IngestJob{
		ID:        uuid.New().String(),
		URL:       url,
		Type:      contentType,
		Title:     title,
		Status:    "queued",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Persist to DB if store is available.
	if store != nil && store.DB != nil {
		if err := store.insertJob(ctx, job); err != nil {
			log.Printf("[vod/ingest] insert job %s: %v", job.ID, err)
		}
	}

	// Register in-memory.
	jobsMu.Lock()
	jobs[job.ID] = job
	jobsMu.Unlock()

	// Run pipeline asynchronously.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
		defer cancel()
		runIngestPipeline(bgCtx, store, job)
	}()

	return job, nil
}

// GetJob returns a copy of the job by ID, or nil if not found.
func GetJob(id string) *IngestJob {
	jobsMu.RLock()
	defer jobsMu.RUnlock()
	if j, ok := jobs[id]; ok {
		cp := *j
		return &cp
	}
	return nil
}

// CancelJob marks a job as failed with a cancelled reason.
// Has no effect if the job is already done or failed.
func CancelJob(id string) bool {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	j, ok := jobs[id]
	if !ok {
		return false
	}
	if j.Status == "done" || j.Status == "failed" {
		return false
	}
	j.Status = "failed"
	j.Error = "cancelled by user"
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return true
}

// ── Pipeline ──────────────────────────────────────────────────────────────────

// runIngestPipeline executes the full download → probe → transcode → upload pipeline.
func runIngestPipeline(ctx context.Context, store *JobStore, job *IngestJob) {
	tempDir := getIngestEnv("VOD_TEMP_DIR", "/tmp/roost-vod")
	jobDir := filepath.Join(tempDir, job.ID)
	if err := os.MkdirAll(jobDir, 0750); err != nil {
		setJobFailed(store, job, fmt.Sprintf("mkdir %s: %v", jobDir, err))
		return
	}
	defer os.RemoveAll(jobDir) // clean up temp files

	// Step 1: Download
	setJobStatus(store, job, "downloading")
	inputPath := filepath.Join(jobDir, "input"+inferExtension(job.URL))
	if err := downloadFile(ctx, job.URL, inputPath); err != nil {
		setJobFailed(store, job, fmt.Sprintf("download: %v", err))
		return
	}

	// Step 2: Probe
	setJobStatus(store, job, "probing")
	probe, err := Probe(inputPath)
	if err != nil {
		setJobFailed(store, job, fmt.Sprintf("probe: %v", err))
		return
	}
	log.Printf("[vod/ingest] job %s: %s — codec=%s res=%dx%d dur=%.0fs",
		job.ID, job.Title, probe.VideoCodec, probe.Width, probe.Height, probe.Duration)

	// Step 3: Transcode to HLS (skip if already HLS)
	hlsDir := filepath.Join(jobDir, "hls")
	if err := os.MkdirAll(hlsDir, 0750); err != nil {
		setJobFailed(store, job, fmt.Sprintf("mkdir hls: %v", err))
		return
	}
	setJobStatus(store, job, "transcoding")
	hlsPlaylist := filepath.Join(hlsDir, "index.m3u8")
	if err := TranscodeToHLS(ctx, inputPath, hlsPlaylist); err != nil {
		setJobFailed(store, job, fmt.Sprintf("transcode: %v", err))
		return
	}

	// Step 4: Upload HLS to R2
	setJobStatus(store, job, "uploading")
	bucket := getIngestEnv("R2_VOD_BUCKET", "roost-vod")
	r2KeyBase := fmt.Sprintf("vod/%s/%s", job.Type, job.ID)
	if err := uploadHLSToR2(ctx, bucket, r2KeyBase, hlsDir); err != nil {
		setJobFailed(store, job, fmt.Sprintf("r2 upload: %v", err))
		return
	}

	r2PlaylistKey := r2KeyBase + "/index.m3u8"

	// Step 5: Update job state
	jobsMu.Lock()
	job.Status = "done"
	job.R2Key = r2PlaylistKey
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	jobsMu.Unlock()

	if store != nil && store.DB != nil {
		_ = store.updateJob(context.Background(), job)
	}

	log.Printf("[vod/ingest] job %s done: r2://%s/%s", job.ID, bucket, r2PlaylistKey)
}

// downloadFile fetches a URL and writes it to destPath.
func downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 4 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// uploadHLSToR2 uploads all files in hlsDir to R2 under keyBase/.
func uploadHLSToR2(ctx context.Context, bucket, keyBase, hlsDir string) error {
	r2Client, err := newR2Client()
	if err != nil {
		return fmt.Errorf("r2 client: %w", err)
	}

	entries, err := os.ReadDir(hlsDir)
	if err != nil {
		return fmt.Errorf("read hls dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filePath := filepath.Join(hlsDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		key := keyBase + "/" + entry.Name()
		contentType := contentTypeForFile(entry.Name())
		if _, err := r2Client.PutObject(bucket, key, data, contentType); err != nil {
			return fmt.Errorf("upload %s: %w", key, err)
		}
		log.Printf("[vod/ingest] uploaded r2://%s/%s", bucket, key)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// setJobStatus updates job status in-memory and in the DB.
func setJobStatus(store *JobStore, job *IngestJob, status string) {
	jobsMu.Lock()
	job.Status = status
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	jobsMu.Unlock()
	if store != nil && store.DB != nil {
		_ = store.updateJob(context.Background(), job)
	}
	log.Printf("[vod/ingest] job %s: %s", job.ID, status)
}

// setJobFailed marks the job as failed with an error message.
func setJobFailed(store *JobStore, job *IngestJob, errMsg string) {
	jobsMu.Lock()
	job.Status = "failed"
	job.Error = errMsg
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	jobsMu.Unlock()
	if store != nil && store.DB != nil {
		_ = store.updateJob(context.Background(), job)
	}
	log.Printf("[vod/ingest] job %s FAILED: %s", job.ID, errMsg)
}

// inferExtension infers a file extension from a URL.
func inferExtension(url string) string {
	u := strings.ToLower(url)
	for _, ext := range []string{".mp4", ".mkv", ".avi", ".mov", ".ts", ".m2ts", ".m4v"} {
		if strings.Contains(u, ext) {
			return ext
		}
	}
	return ".mp4"
}

// contentTypeForFile returns the MIME type for an HLS file.
func contentTypeForFile(name string) string {
	switch {
	case strings.HasSuffix(name, ".m3u8"):
		return "application/x-mpegurl"
	case strings.HasSuffix(name, ".ts"):
		return "video/MP2T"
	case strings.HasSuffix(name, ".mp4"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// getIngestEnv returns an env var with a fallback.
func getIngestEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── DB persistence ────────────────────────────────────────────────────────────

// insertJob persists a new job to the ingest_jobs table.
func (s *JobStore) insertJob(ctx context.Context, job *IngestJob) error {
	meta, _ := json.Marshal(map[string]string{"title": job.Title, "type": job.Type})
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO ingest_jobs (id, source_url, content_type, title, status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, job.ID, job.URL, job.Type, job.Title, job.Status, meta)
	return err
}

// updateJob updates the job status and r2 key in the DB.
func (s *JobStore) updateJob(ctx context.Context, job *IngestJob) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE ingest_jobs
		SET status = $1, r2_key = $2, error_message = $3, updated_at = NOW()
		WHERE id = $4
	`, job.Status, nullStr(job.R2Key), nullStr(job.Error), job.ID)
	return err
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
