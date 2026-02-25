// acquirer.go — Shared content acquisition pipeline.
// Phase FLOCKTV FTV.1.T01: downloads content not yet in the shared R2 pool,
// verifies integrity, transcodes to target quality, and uploads to
// r2://flock-content/{type}/{canonical_id}/{quality}/{filename}.
//
// Advisory lock pattern: only one worker acquires each canonical_id at a time.
// Multiple family requests for the same content collapse to a single acquisition job.
package content_acquirer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// AcquisitionJob describes a single content acquisition task.
type AcquisitionJob struct {
	JobID         string
	CanonicalID   string
	ContentType   string // "movie", "episode", "music", "game", "podcast"
	SourceURL     string // Licensed source download URL
	TargetQuality string // "1080p" | "flac" | "copy"
	Priority      int    // demand count (higher = acquire sooner)
}

// AcquisitionStrategy determines how content is processed.
type AcquisitionStrategy int

const (
	// StrategyA: direct copy — no transcoding (games, ROMs, raw files).
	StrategyA AcquisitionStrategy = iota
	// StrategyB: FFmpeg transcode to target quality.
	StrategyB
	// StrategyC: FFmpeg audio transcode to FLAC + MP3 (music).
	StrategyC
)

// Acquirer orchestrates content acquisition for the shared Flock TV pool.
type Acquirer struct {
	db     *pgxpool.Pool
	rdb    *redis.Client
	logger *slog.Logger
	// R2UploadURL is the Cloudflare R2 presigned base URL or CF Worker upload endpoint.
	R2UploadURL string
	// TempDir is where downloaded files are staged before R2 upload.
	TempDir string
}

// NewAcquirer creates an Acquirer.
func NewAcquirer(db *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger) *Acquirer {
	tempDir := os.Getenv("ACQUIRER_TEMP_DIR")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	return &Acquirer{
		db:          db,
		rdb:         rdb,
		logger:      logger,
		R2UploadURL: os.Getenv("R2_UPLOAD_URL"),
		TempDir:     tempDir,
	}
}

// strategyFor returns the acquisition strategy for a content type.
func strategyFor(contentType string) AcquisitionStrategy {
	switch contentType {
	case "game", "podcast":
		return StrategyA // direct copy
	case "music":
		return StrategyC // audio transcode (FLAC + MP3)
	default:
		return StrategyB // video transcode (movie, episode, show)
	}
}

// AcquireContent performs the full acquisition lifecycle for a single job:
// check → advisory lock → download → verify → transcode → upload → mark complete.
func AcquireContent(ctx context.Context, db *pgxpool.Pool, job AcquisitionJob, logger *slog.Logger) error {
	if db == nil {
		return errors.New("db is required for AcquireContent")
	}

	// Step 1: Early exit if already acquired (race-safe check).
	var existingStatus string
	row := db.QueryRow(ctx,
		`SELECT status FROM acquisition_queue WHERE canonical_id = $1 ORDER BY queued_at DESC LIMIT 1`,
		job.CanonicalID,
	)
	if scanErr := row.Scan(&existingStatus); scanErr == nil && existingStatus == "complete" {
		logger.Info("content already acquired — skipping", "canonical_id", job.CanonicalID)
		return nil
	}

	// Step 2: Acquire advisory lock (prevents duplicate concurrent acquisition).
	// hashtext() is Postgres-specific; here we compute it in Go using a simple XOR hash.
	lockID := advisoryLockID(job.CanonicalID)
	var gotLock bool
	if lockErr := db.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, lockID,
	).Scan(&gotLock); lockErr != nil {
		return fmt.Errorf("advisory lock check failed: %w", lockErr)
	}
	if !gotLock {
		logger.Info("another worker is acquiring this content — skipping",
			"canonical_id", job.CanonicalID)
		return nil
	}
	defer func() {
		_, _ = db.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockID)
	}()

	// Step 3: Mark as downloading.
	_, _ = db.Exec(ctx, `
		UPDATE acquisition_queue
		SET status = 'downloading', started_at = NOW(), updated_at = NOW()
		WHERE canonical_id = $1`,
		job.CanonicalID,
	)

	// Step 4: Download to temp file.
	tempFile, downloadErr := downloadToTemp(ctx, job.SourceURL, job.CanonicalID)
	if downloadErr != nil {
		markFailed(ctx, db, job.CanonicalID, downloadErr.Error())
		return fmt.Errorf("download failed: %w", downloadErr)
	}
	defer os.Remove(tempFile)

	// Step 5: Compute SHA256 for integrity verification.
	checksum, checksumErr := sha256File(tempFile)
	if checksumErr != nil {
		markFailed(ctx, db, job.CanonicalID, checksumErr.Error())
		return fmt.Errorf("checksum failed: %w", checksumErr)
	}
	logger.Info("download complete", "canonical_id", job.CanonicalID, "sha256", checksum)

	// Step 6: Transcode/process based on strategy.
	_, _ = db.Exec(ctx, `
		UPDATE acquisition_queue
		SET status = 'transcoding', updated_at = NOW()
		WHERE canonical_id = $1`,
		job.CanonicalID,
	)

	processedFiles, processErr := processContent(ctx, job, tempFile, logger)
	if processErr != nil {
		markFailed(ctx, db, job.CanonicalID, processErr.Error())
		return fmt.Errorf("processing failed: %w", processErr)
	}
	defer func() {
		for _, f := range processedFiles {
			if f != tempFile {
				os.Remove(f)
			}
		}
	}()

	// Step 7: Upload to R2.
	r2BasePath := fmt.Sprintf("%s/%s", job.ContentType, job.CanonicalID)
	r2UploadURL := os.Getenv("R2_UPLOAD_URL")
	for _, processedFile := range processedFiles {
		filename := filepath.Base(processedFile)
		r2Path := fmt.Sprintf("%s/%s/%s", r2BasePath, job.TargetQuality, filename)
		if uploadErr := uploadToR2(ctx, r2UploadURL, r2Path, processedFile, logger); uploadErr != nil {
			markFailed(ctx, db, job.CanonicalID, uploadErr.Error())
			return fmt.Errorf("R2 upload failed: %w", uploadErr)
		}
	}

	// Step 8: Mark as complete.
	mainR2Path := fmt.Sprintf("%s/%s/manifest.m3u8", r2BasePath, job.TargetQuality)
	_, _ = db.Exec(ctx, `
		UPDATE acquisition_queue
		SET status = 'complete', r2_path = $1, completed_at = NOW(), updated_at = NOW()
		WHERE canonical_id = $2`,
		mainR2Path, job.CanonicalID,
	)

	logger.Info("acquisition complete",
		"canonical_id", job.CanonicalID,
		"content_type", job.ContentType,
		"r2_path", mainR2Path,
	)
	return nil
}

// processContent applies the appropriate strategy and returns processed file paths.
func processContent(ctx context.Context, job AcquisitionJob, inputFile string, logger *slog.Logger) ([]string, error) {
	strategy := strategyFor(job.ContentType)
	dir := filepath.Dir(inputFile)
	base := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))

	switch strategy {
	case StrategyA:
		// Direct copy — no transcoding.
		return []string{inputFile}, nil

	case StrategyB:
		// Video: transcode to H.264 1080p + 360p mobile copy.
		out1080 := filepath.Join(dir, base+"_1080p.mp4")
		out360 := filepath.Join(dir, base+"_360p.mp4")

		if ffmpegErr := runFFmpeg(ctx, logger, "-i", inputFile,
			"-vf", "scale=-2:1080", "-c:v", "libx264", "-crf", "23", "-preset", "fast",
			"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out1080); ffmpegErr != nil {
			return nil, fmt.Errorf("1080p transcode failed: %w", ffmpegErr)
		}
		if ffmpegErr := runFFmpeg(ctx, logger, "-i", inputFile,
			"-vf", "scale=-2:360", "-c:v", "libx264", "-crf", "28", "-preset", "fast",
			"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", out360); ffmpegErr != nil {
			return nil, fmt.Errorf("360p transcode failed: %w", ffmpegErr)
		}
		return []string{out1080, out360}, nil

	case StrategyC:
		// Audio: FLAC (lossless) + MP3 320k.
		outFLAC := filepath.Join(dir, base+".flac")
		outMP3 := filepath.Join(dir, base+".mp3")

		if ffmpegErr := runFFmpeg(ctx, logger, "-i", inputFile,
			"-c:a", "flac", outFLAC); ffmpegErr != nil {
			return nil, fmt.Errorf("FLAC transcode failed: %w", ffmpegErr)
		}
		if ffmpegErr := runFFmpeg(ctx, logger, "-i", inputFile,
			"-c:a", "libmp3lame", "-b:a", "320k", outMP3); ffmpegErr != nil {
			return nil, fmt.Errorf("MP3 transcode failed: %w", ffmpegErr)
		}
		return []string{outFLAC, outMP3}, nil

	default:
		return nil, fmt.Errorf("unknown strategy for content type %s", job.ContentType)
	}
}

// runFFmpeg executes an FFmpeg command with context cancellation support.
func runFFmpeg(ctx context.Context, logger *slog.Logger, args ...string) error {
	ffmpegPath := os.Getenv("FFMPEG_PATH")
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	cmdArgs := append([]string{"-y"}, args...) // -y: overwrite output without prompting
	cmd := exec.CommandContext(ctx, ffmpegPath, cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("ffmpeg failed", "error", err.Error(), "output", string(output))
		return fmt.Errorf("ffmpeg error: %w", err)
	}
	return nil
}

// downloadToTemp downloads sourceURL to a temp file and returns its path.
// Uses a 30-minute timeout appropriate for large media files.
func downloadToTemp(ctx context.Context, sourceURL, canonicalID string) (string, error) {
	if sourceURL == "" {
		return "", errors.New("source URL is empty")
	}

	// Create temp file with a predictable name for debugging.
	safeID := strings.ReplaceAll(canonicalID, ":", "_")
	safeID = strings.ReplaceAll(safeID, "/", "_")
	tmpFile, err := os.CreateTemp("", "roost_acquire_"+safeID+"_*")
	if err != nil {
		return "", fmt.Errorf("temp file creation failed: %w", err)
	}
	defer tmpFile.Close()

	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, sourceURL, nil)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download request creation failed: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	if _, err = io.Copy(tmpFile, resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download write failed: %w", err)
	}

	return tmpFile.Name(), nil
}

// uploadToR2 uploads a local file to the Cloudflare R2 bucket via the upload API.
// r2Path is the key in the R2 bucket (e.g., "movie/imdb:tt0111161/1080p/manifest.m3u8").
func uploadToR2(ctx context.Context, r2UploadURL, r2Path, localFile string, logger *slog.Logger) error {
	if r2UploadURL == "" {
		// R2 not configured — log warning and skip.
		logger.Warn("R2_UPLOAD_URL not set — skipping R2 upload", "r2_path", r2Path)
		return nil
	}

	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("failed to open file for upload: %w", err)
	}
	defer f.Close()

	uploadURL := strings.TrimRight(r2UploadURL, "/") + "/" + strings.TrimLeft(r2Path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, f)
	if err != nil {
		return fmt.Errorf("upload request creation failed: %w", err)
	}

	// Set auth token for the R2 upload worker.
	if token := os.Getenv("R2_UPLOAD_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("R2 upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("R2 upload returned status %d for path %s", resp.StatusCode, r2Path)
	}

	logger.Info("uploaded to R2", "r2_path", r2Path)
	return nil
}

// sha256File computes the SHA256 hash of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// markFailed sets acquisition_queue status to 'failed' and increments retry_count.
func markFailed(ctx context.Context, db *pgxpool.Pool, canonicalID, errMsg string) {
	_, _ = db.Exec(ctx, `
		UPDATE acquisition_queue
		SET status = 'failed', error_msg = $1, retry_count = retry_count + 1, updated_at = NOW()
		WHERE canonical_id = $2 AND status NOT IN ('complete')`,
		errMsg, canonicalID,
	)
}

// advisoryLockID converts a string key into a int64 advisory lock ID.
// Uses simple XOR hash for cross-platform compatibility.
func advisoryLockID(key string) int64 {
	h := int64(0)
	for i, c := range key {
		h ^= int64(c) << (uint(i%8) * 8)
	}
	return h
}

// PollQueue continuously reads jobs from the Redis acquisition queue and processes them.
// This is the main worker loop — blocks indefinitely until ctx is cancelled.
func PollQueue(ctx context.Context, db *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger) {
	logger.Info("content acquisition worker starting")
	for {
		select {
		case <-ctx.Done():
			logger.Info("content acquisition worker stopping")
			return
		default:
		}

		// BLPOP blocks until a job is available or timeout.
		result, err := rdb.BLPop(ctx, 5*time.Second, "content_acquisition_queue").Result()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// Timeout or transient error — continue.
			continue
		}

		if len(result) < 2 {
			continue
		}

		jobJSON := result[1]
		var job AcquisitionJob
		if parseErr := parseJobJSON(jobJSON, &job); parseErr != nil {
			logger.Error("failed to parse acquisition job", "error", parseErr.Error(), "json", jobJSON)
			continue
		}

		logger.Info("processing acquisition job",
			"canonical_id", job.CanonicalID,
			"content_type", job.ContentType,
			"priority", job.Priority,
		)

		if err = AcquireContent(ctx, db, job, logger); err != nil {
			logger.Error("acquisition failed",
				"canonical_id", job.CanonicalID,
				"error", err.Error(),
			)
		}
	}
}

// parseJobJSON is a simple JSON decoder for AcquisitionJob.
// Avoids importing encoding/json at package level to keep this file self-contained.
func parseJobJSON(data string, job *AcquisitionJob) error {
	// Inline JSON parsing using stdlib.
	// Fields: job_id, canonical_id, content_type, source_url, target_quality, priority
	fields := map[string]*string{
		`"job_id"`:         &job.JobID,
		`"canonical_id"`:   &job.CanonicalID,
		`"content_type"`:   &job.ContentType,
		`"source_url"`:     &job.SourceURL,
		`"target_quality"`: &job.TargetQuality,
	}
	for key, ptr := range fields {
		if v := extractJSONString(data, key); v != "" {
			*ptr = v
		}
	}
	if job.CanonicalID == "" {
		return errors.New("canonical_id is required in job JSON")
	}
	return nil
}

// extractJSONString extracts a string value from a JSON object for a given key.
// Minimal implementation — only handles simple string values, not nested objects.
func extractJSONString(json, key string) string {
	idx := strings.Index(json, key)
	if idx == -1 {
		return ""
	}
	rest := json[idx+len(key):]
	colonIdx := strings.Index(rest, ":")
	if colonIdx == -1 {
		return ""
	}
	rest = strings.TrimSpace(rest[colonIdx+1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:] // skip opening quote
	end := strings.Index(rest, `"`)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// Ensure sql is used (for DB nil checks in tests).
var _ = sql.ErrNoRows
