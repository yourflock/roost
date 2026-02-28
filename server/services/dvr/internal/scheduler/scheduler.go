// scheduler.go — DVR recording scheduler.
// Polls the database for scheduled recordings that should start, executes them
// by capturing live HLS segments, then marks them complete and uploads to storage.
//
// Design:
//   - Poll every 30 seconds for recordings with start_time ≤ now AND status='scheduled'
//   - Spawn a capture goroutine per recording (copies segments from ingest's segment dir)
//   - At end_time, concatenate segments into a VOD HLS playlist
//   - Upload to object storage (Hetzner Object Storage / S3-compatible)
//   - Update status to 'complete' with storage_path + file_size_bytes
package scheduler

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Recording mirrors the dvr_recordings table row (relevant fields).
type Recording struct {
	ID           string
	SubscriberID string
	ChannelID    string
	ChannelSlug  string // joined from channels table
	Title        string
	StartTime    time.Time
	EndTime      time.Time
	Status       string
}

// QuotaInfo represents a subscriber's DVR quota usage.
type QuotaInfo struct {
	UsedHours float64 `json:"used_hours"`
	MaxHours  float64 `json:"max_hours"`
	RemainingHours float64 `json:"remaining_hours"`
	PlanName  string  `json:"plan_name"`
}

// planQuotaHours maps Roost plan names to DVR quota hours.
var planQuotaHours = map[string]float64{
	"basic":   10.0,
	"premium": 50.0,
	"family":  100.0,
	"founder": 100.0, // founders get family quota
}

// defaultQuotaHours is used when the plan is unknown.
const defaultQuotaHours = 10.0

// Config holds scheduler configuration.
type Config struct {
	SegmentDir string // ingest segment directory (read-only)
	DVRDir     string // local scratch for DVR captures
	StorageDir string // upload destination (local or S3 path prefix)
	PollEvery  time.Duration
}

// Scheduler watches for pending recordings and executes them.
type Scheduler struct {
	cfg     Config
	db      *sql.DB
	mu      sync.Mutex
	active  map[string]context.CancelFunc // recording ID → cancel
}

// New creates a Scheduler.
func New(cfg Config, db *sql.DB) *Scheduler {
	return &Scheduler{
		cfg:    cfg,
		db:     db,
		active: make(map[string]context.CancelFunc),
	}
}

// Run starts the scheduler's poll loop. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollEvery)
	defer ticker.Stop()
	log.Printf("[dvr] scheduler started, polling every %s", s.cfg.PollEvery)
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

// poll queries for newly due recordings and launches capture goroutines.
func (s *Scheduler) poll(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.subscriber_id, r.channel_id, c.slug, r.title, r.start_time, r.end_time
		FROM dvr_recordings r
		JOIN channels c ON c.id = r.channel_id
		WHERE r.status = 'scheduled' AND r.start_time <= NOW()
		ORDER BY r.start_time ASC
		LIMIT 20`)
	if err != nil {
		log.Printf("[dvr] poll query error: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var rec Recording
		if err := rows.Scan(&rec.ID, &rec.SubscriberID, &rec.ChannelID, &rec.ChannelSlug,
			&rec.Title, &rec.StartTime, &rec.EndTime); err != nil {
			continue
		}
		s.mu.Lock()
		_, alreadyActive := s.active[rec.ID]
		s.mu.Unlock()
		if alreadyActive {
			continue
		}
		s.startCapture(ctx, rec)
	}
}

// startCapture claims a recording and launches its capture goroutine.
func (s *Scheduler) startCapture(ctx context.Context, rec Recording) {
	// Claim atomically: update status to 'recording' only if still 'scheduled'.
	res, err := s.db.ExecContext(ctx, `
		UPDATE dvr_recordings SET status='recording', updated_at=NOW()
		WHERE id=$1 AND status='scheduled'`, rec.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // another process claimed it
	}

	recCtx, cancel := context.WithDeadline(ctx, rec.EndTime.Add(30*time.Second))
	s.mu.Lock()
	s.active[rec.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.active, rec.ID)
			s.mu.Unlock()
		}()
		if err := s.capture(recCtx, rec); err != nil {
			log.Printf("[dvr] capture failed for %s: %v", rec.ID, err)
			_, _ = s.db.ExecContext(context.Background(), `
				UPDATE dvr_recordings SET status='failed', updated_at=NOW() WHERE id=$1`, rec.ID)
		}
	}()
}

// capture copies HLS segments produced by the ingest service for the duration
// of a recording, then assembles them into a VOD HLS playlist.
func (s *Scheduler) capture(ctx context.Context, rec Recording) error {
	scratchDir := filepath.Join(s.cfg.DVRDir, rec.ID)
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return fmt.Errorf("mkdir scratch: %w", err)
	}
	defer os.RemoveAll(scratchDir)

	channelSegDir := filepath.Join(s.cfg.SegmentDir, rec.ChannelSlug)
	seen := make(map[string]bool)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var copiedSegments []string

	// Capture loop: poll segment directory until end_time.
	for {
		select {
		case <-ctx.Done():
			// Normal end: ctx deadline = rec.EndTime + 30s
		case <-ticker.C:
			segments, _ := filepath.Glob(filepath.Join(channelSegDir, "*.ts"))
			sort.Strings(segments)
			for _, seg := range segments {
				if seen[seg] {
					continue
				}
				seen[seg] = true
				dst := filepath.Join(scratchDir, filepath.Base(seg))
				if err := copyFile(seg, dst); err == nil {
					copiedSegments = append(copiedSegments, filepath.Base(seg))
				}
			}
			if time.Now().After(rec.EndTime) {
				goto done
			}
			continue
		}
		break
	}
done:
	// Final segment sweep after end time
	segments, _ := filepath.Glob(filepath.Join(channelSegDir, "*.ts"))
	sort.Strings(segments)
	for _, seg := range segments {
		if !seen[seg] {
			seen[seg] = true
			dst := filepath.Join(scratchDir, filepath.Base(seg))
			if err := copyFile(seg, dst); err == nil {
				copiedSegments = append(copiedSegments, filepath.Base(seg))
			}
		}
	}

	if len(copiedSegments) == 0 {
		return fmt.Errorf("no segments captured for recording %s", rec.ID)
	}

	// Generate VOD HLS playlist
	playlistPath := filepath.Join(scratchDir, "recording.m3u8")
	if err := generateVODPlaylist(playlistPath, copiedSegments, scratchDir); err != nil {
		return fmt.Errorf("playlist generation: %w", err)
	}

	// Calculate total size
	var totalBytes int64
	allFiles := append([]string{playlistPath}, func() []string {
		var ts []string
		for _, seg := range copiedSegments {
			ts = append(ts, filepath.Join(scratchDir, seg))
		}
		return ts
	}()...)
	for _, f := range allFiles {
		if fi, err := os.Stat(f); err == nil {
			totalBytes += fi.Size()
		}
	}

	// Persist to storage (local DVR storage directory for now;
	// production would upload to S3/Object Storage)
	storagePath := filepath.Join(s.cfg.StorageDir, rec.SubscriberID, rec.ID)
	if err := os.MkdirAll(storagePath, 0o755); err != nil {
		return fmt.Errorf("mkdir storage: %w", err)
	}
	for _, f := range copiedSegments {
		src := filepath.Join(scratchDir, f)
		dst := filepath.Join(storagePath, f)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy segment to storage: %w", err)
		}
	}
	finalPlaylist := filepath.Join(storagePath, "recording.m3u8")
	if err := copyFile(playlistPath, finalPlaylist); err != nil {
		return fmt.Errorf("copy playlist to storage: %w", err)
	}

	// Mark complete in DB
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE dvr_recordings
		SET status='complete', storage_path=$2, file_size_bytes=$3, updated_at=NOW()
		WHERE id=$1`, rec.ID, finalPlaylist, totalBytes)
	if err != nil {
		return fmt.Errorf("db update complete: %w", err)
	}

	log.Printf("[dvr] recording %s complete: %d segments, %.2f MB",
		rec.ID, len(copiedSegments), float64(totalBytes)/(1024*1024))
	return nil
}

// cancelAll cancels all active captures (called on shutdown).
func (s *Scheduler) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.active {
		cancel()
	}
}

// generateVODPlaylist writes a HLS VOD playlist from a list of .ts filenames.
func generateVODPlaylist(path string, segments []string, segDir string) error {
	sort.Strings(segments)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "#EXTM3U")
	fmt.Fprintln(w, "#EXT-X-VERSION:3")
	fmt.Fprintln(w, "#EXT-X-TARGETDURATION:10")
	fmt.Fprintln(w, "#EXT-X-PLAYLIST-TYPE:VOD")
	for _, seg := range segments {
		fmt.Fprintln(w, "#EXTINF:8.000,")
		fmt.Fprintln(w, seg)
	}
	fmt.Fprintln(w, "#EXT-X-ENDLIST")
	return w.Flush()
}

// copyFile copies src → dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// -- Quota helpers used by the HTTP layer ------------------------------------

// SubscriberPlan looks up a subscriber's plan name from the DB.
func SubscriberPlan(ctx context.Context, db *sql.DB, subscriberID string) string {
	var plan sql.NullString
	_ = db.QueryRowContext(ctx, `
		SELECT p.name FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.subscriber_id = $1 AND s.status IN ('active','trialing')
		ORDER BY s.created_at DESC LIMIT 1`, subscriberID).Scan(&plan)
	if plan.Valid {
		return strings.ToLower(plan.String)
	}
	return "basic"
}

// GetQuota returns quota info for a subscriber.
func GetQuota(ctx context.Context, db *sql.DB, subscriberID string) (*QuotaInfo, error) {
	planName := SubscriberPlan(ctx, db, subscriberID)
	maxHours, ok := planQuotaHours[planName]
	if !ok {
		maxHours = defaultQuotaHours
	}

	var usedHours float64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(ROUND(SUM(EXTRACT(EPOCH FROM (end_time - start_time)) / 3600)::NUMERIC, 2), 0)
		FROM dvr_recordings
		WHERE subscriber_id = $1 AND status IN ('scheduled','recording','complete')`,
		subscriberID).Scan(&usedHours)
	if err != nil {
		return nil, err
	}

	return &QuotaInfo{
		UsedHours:      usedHours,
		MaxHours:       maxHours,
		RemainingHours: max(0, maxHours-usedHours),
		PlanName:       planName,
	}, nil
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// WriteJSON encodes v as JSON to w with the given status code.
func WriteJSON(w io.Writer, v interface{}) error {
	return json.NewEncoder(w).Encode(v)
}

// UUIDNew generates a new UUID string.
func UUIDNew() string {
	return uuid.New().String()
}
