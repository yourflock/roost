// main.go — Roost Catchup Service.
// Archives live HLS segments to rolling 7-day storage, enabling time-shifted
// and catch-up viewing of live channels. Generates per-hour m3u8 playlists from
// archived segments. Cleanup job prunes segments beyond retention window.
// Port: 8098 (env: CATCHUP_PORT). Internal service — not exposed to subscribers directly.
//
// Routes:
//   GET  /catchup/:channel_slug                               — list available hours for channel
//   GET  /catchup/:channel_slug/playlist.m3u8?start=...&end=  — time-range playlist (HLS VOD)
//   GET  /catchup/status                                      — all channels archive status
//   POST /internal/catchup/cleanup                            — trigger cleanup (internal only)
//   GET  /health
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// ---- config -----------------------------------------------------------------

type config struct {
	Port           string
	StorageDir     string
	SegmentDir     string // ingest segments (source)
	PostgresURL    string
	RetentionDays  int
	PollInterval   time.Duration
	CleanupEvery   time.Duration
}

func loadConfig() config {
	retDays, _ := strconv.Atoi(getEnv("CATCHUP_RETENTION_DAYS", "7"))
	if retDays < 1 { retDays = 7 }
	return config{
		Port:          getEnv("CATCHUP_PORT", "8098"),
		StorageDir:    getEnv("CATCHUP_STORAGE_DIR", "/var/roost/catchup"),
		SegmentDir:    getEnv("SEGMENT_DIR", "/var/roost/segments"),
		PostgresURL:   getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"),
		RetentionDays: retDays,
		PollInterval:  10 * time.Second,
		CleanupEvery:  6 * time.Hour,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---- db helpers -------------------------------------------------------------

func connectDB(url string) (*sql.DB, error) {
	db, err := sql.Open("postgres", url)
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

// ---- archiver ---------------------------------------------------------------

// archiver watches the ingest segment directory and copies new segments to
// the catchup archive organized as {channel_slug}/{YYYY-MM-DD}/{HH}/segment_NNNN.ts.
// It also generates per-hour m3u8 playlists.
type archiver struct {
	cfg      config
	db       *sql.DB
	seen     map[string]bool // segmentPath → archived
}

func newArchiver(cfg config, db *sql.DB) *archiver {
	return &archiver{cfg: cfg, db: db, seen: make(map[string]bool)}
}

// run polls the segment directory for new .ts files and archives them.
func (a *archiver) run(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.scanAndArchive(ctx)
		}
	}
}

func (a *archiver) scanAndArchive(ctx context.Context) {
	// Walk {segmentDir}/{channel_slug}/*.ts
	channels, err := os.ReadDir(a.cfg.SegmentDir)
	if err != nil {
		return
	}
	for _, ch := range channels {
		if !ch.IsDir() { continue }
		channelSlug := ch.Name()
		// Check if catchup is enabled for this channel
		var enabled bool
		_ = a.db.QueryRowContext(ctx,
			`SELECT cs.enabled FROM catchup_settings cs
			 JOIN channels c ON c.id = cs.channel_id
			 WHERE c.slug = $1`, channelSlug).Scan(&enabled)
		if !enabled { continue }

		channelDir := filepath.Join(a.cfg.SegmentDir, channelSlug)
		segments, err := filepath.Glob(filepath.Join(channelDir, "*.ts"))
		if err != nil { continue }

		for _, seg := range segments {
			if a.seen[seg] { continue }
			if err := a.archiveSegment(ctx, channelSlug, seg); err == nil {
				a.seen[seg] = true
			}
		}
	}
}

// archiveSegment copies a single .ts segment into the catchup archive.
func (a *archiver) archiveSegment(ctx context.Context, channelSlug, segPath string) error {
	now := time.Now().UTC()
	dateStr := now.Format("2006-01-02")
	hourStr := fmt.Sprintf("%02d", now.Hour())

	destDir := filepath.Join(a.cfg.StorageDir, channelSlug, dateStr, hourStr)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	destPath := filepath.Join(destDir, filepath.Base(segPath))
	if err := copyFile(segPath, destPath); err != nil {
		return err
	}

	// Get file size for DB tracking
	info, _ := os.Stat(destPath)
	var fileSize int64
	if info != nil { fileSize = info.Size() }

	// Upsert catchup_recordings row (track segment count + bytes)
	var channelID string
	if err := a.db.QueryRowContext(ctx,
		`SELECT id FROM channels WHERE slug = $1`, channelSlug).Scan(&channelID); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO catchup_recordings (id, channel_id, date, hour, segment_count, total_bytes, status)
		VALUES ($1, $2, $3, $4, 1, $5, 'recording')
		ON CONFLICT (channel_id, date, hour) DO UPDATE SET
			segment_count = catchup_recordings.segment_count + 1,
			total_bytes   = catchup_recordings.total_bytes + $5`,
		uuid.New().String(), channelID, dateStr, now.Hour(), fileSize)
	if err != nil {
		return err
	}

	// Regenerate hourly m3u8 playlist
	return a.generateHourPlaylist(ctx, channelSlug, dateStr, hourStr, destDir)
}

// generateHourPlaylist creates/overwrites the per-hour m3u8 from all .ts files in a directory.
func (a *archiver) generateHourPlaylist(_ context.Context, channelSlug, dateStr, hourStr, dir string) error {
	segs, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil || len(segs) == 0 {
		return err
	}
	sort.Strings(segs)

	playlistPath := filepath.Join(dir, "playlist.m3u8")
	f, err := os.Create(playlistPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "#EXTM3U")
	fmt.Fprintln(w, "#EXT-X-VERSION:3")
	fmt.Fprintln(w, "#EXT-X-TARGETDURATION:10")
	fmt.Fprintf(w, "#EXT-X-PROGRAM-DATE-TIME:%sT%s:00:00Z\n", dateStr, hourStr)
	for _, seg := range segs {
		fmt.Fprintln(w, "#EXTINF:8.000,")
		fmt.Fprintln(w, filepath.Base(seg))
	}
	fmt.Fprintln(w, "#EXT-X-ENDLIST")
	return w.Flush()
}

// copyFile copies src to dst, creating intermediate directories.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil { return err }
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ---- cleanup ----------------------------------------------------------------

// runCleanup deletes segments (and empty directories) older than retention window.
func (a *archiver) runCleanup(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.CleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.cleanup(ctx)
		}
	}
}

func (a *archiver) cleanup(ctx context.Context) {
	cutoff := time.Now().UTC().AddDate(0, 0, -a.cfg.RetentionDays)
	log.Printf("[catchup] cleanup: removing segments older than %s", cutoff.Format("2006-01-02"))

	var totalBytes int64
	var totalFiles int

	channelDirs, err := os.ReadDir(a.cfg.StorageDir)
	if err != nil { return }

	for _, ch := range channelDirs {
		if !ch.IsDir() { continue }
		channelPath := filepath.Join(a.cfg.StorageDir, ch.Name())
		dateDirs, err := os.ReadDir(channelPath)
		if err != nil { continue }

		for _, dd := range dateDirs {
			if !dd.IsDir() { continue }
			t, err := time.Parse("2006-01-02", dd.Name())
			if err != nil || t.After(cutoff) { continue }

			datePath := filepath.Join(channelPath, dd.Name())
			// Count bytes before deleting
			_ = filepath.Walk(datePath, func(path string, info os.FileInfo, _ error) error {
				if info != nil && !info.IsDir() {
					totalBytes += info.Size()
					totalFiles++
				}
				return nil
			})
			os.RemoveAll(datePath)

			// Mark archived/deleted in DB
			_, _ = a.db.ExecContext(ctx, `
				UPDATE catchup_recordings cr
				SET status = 'archived'
				FROM channels c
				WHERE c.id = cr.channel_id AND c.slug = $1 AND cr.date = $2`,
				ch.Name(), dd.Name())
		}

		// Remove empty channel directory
		if entries, _ := os.ReadDir(channelPath); len(entries) == 0 {
			os.Remove(channelPath)
		}
	}
	log.Printf("[catchup] cleanup complete: removed %d files, %.2f MB",
		totalFiles, float64(totalBytes)/(1024*1024))
}

// ---- playlist generation from time range ------------------------------------

// timeRangePlaylist generates an HLS VOD playlist spanning start→end by
// stitching together archived per-hour playlists. Gaps (missing hours) are
// noted but don't cause failure — the playlist skips them.
func (a *archiver) timeRangePlaylist(channelSlug string, start, end time.Time) (string, error) {
	if end.Before(start) || end.Sub(start) > 8*time.Hour {
		return "", fmt.Errorf("invalid time range (max 8 hours)")
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-TARGETDURATION:10\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString(fmt.Sprintf("#EXT-X-PROGRAM-DATE-TIME:%s\n", start.Format(time.RFC3339)))

	current := start.Truncate(time.Hour)
	for current.Before(end) {
		dateStr := current.Format("2006-01-02")
		hourStr := fmt.Sprintf("%02d", current.Hour())
		playlistPath := filepath.Join(a.cfg.StorageDir, channelSlug, dateStr, hourStr, "playlist.m3u8")

		if content, err := os.ReadFile(playlistPath); err == nil {
			for _, line := range strings.Split(string(content), "\n") {
				if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#EXT-X-ENDLIST") {
					if !strings.HasPrefix(line, "#EXTM3U") {
						b.WriteString(line + "\n")
					}
				} else if strings.HasSuffix(line, ".ts") {
					// Rewrite segment path to absolute
					segDir := filepath.Join(a.cfg.StorageDir, channelSlug, dateStr, hourStr)
					b.WriteString("#EXTINF:8.000,\n")
					b.WriteString(filepath.Join(segDir, line) + "\n")
				}
			}
		}
		current = current.Add(time.Hour)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String(), nil
}

// ---- HTTP handlers ----------------------------------------------------------

type handlerSet struct {
	cfg      config
	db       *sql.DB
	archiver *archiver
}

func (h *handlerSet) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-catchup"})
}

func (h *handlerSet) handleChannelAvailability(w http.ResponseWriter, r *http.Request) {
	// /catchup/{channel_slug} — list available date/hour records
	channelSlug := strings.TrimPrefix(r.URL.Path, "/catchup/")
	channelSlug = strings.TrimSuffix(channelSlug, "/")
	if channelSlug == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "channel_slug required")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cr.date, cr.hour, cr.segment_count, cr.total_bytes, cr.status
		FROM catchup_recordings cr
		JOIN channels c ON c.id = cr.channel_id
		WHERE c.slug = $1 AND cr.status IN ('recording','complete')
		ORDER BY cr.date DESC, cr.hour DESC
		LIMIT 200`, channelSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type hourEntry struct {
		Date         string `json:"date"`
		Hour         int    `json:"hour"`
		SegmentCount int    `json:"segment_count"`
		TotalMB      float64 `json:"total_mb"`
		Status       string `json:"status"`
	}
	var entries []hourEntry
	for rows.Next() {
		var e hourEntry
		var totalBytes int64
		var date time.Time
		if err := rows.Scan(&date, &e.Hour, &e.SegmentCount, &totalBytes, &e.Status); err != nil {
			continue
		}
		e.Date = date.Format("2006-01-02")
		e.TotalMB = float64(totalBytes) / (1024 * 1024)
		entries = append(entries, e)
	}
	if entries == nil { entries = []hourEntry{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channel": channelSlug,
		"hours":   entries,
		"count":   len(entries),
	})
}

func (h *handlerSet) handleTimeRangePlaylist(w http.ResponseWriter, r *http.Request) {
	// /catchup/{channel_slug}/playlist.m3u8?start=RFC3339&end=RFC3339
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}
	channelSlug := parts[1]

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	if startStr == "" || endStr == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "start and end query params required (RFC3339)")
		return
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid start time (use RFC3339)")
		return
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid end time (use RFC3339)")
		return
	}
	// Check retention window
	if start.Before(time.Now().UTC().AddDate(0, 0, -h.cfg.RetentionDays)) {
		writeError(w, http.StatusGone, "expired", "Content outside retention window")
		return
	}

	playlist, err := h.archiver.timeRangePlaylist(channelSlug, start, end)
	if err != nil {
		writeError(w, http.StatusBadRequest, "range_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(playlist))
}

func (h *handlerSet) handleStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.slug, c.name,
		       COALESCE(cs.enabled, false) AS catchup_enabled,
		       COALESCE(cs.retention_days, $1) AS retention_days,
		       COUNT(cr.id) AS recording_hours,
		       COALESCE(SUM(cr.total_bytes), 0) AS total_bytes,
		       MIN(cr.date) AS oldest_date
		FROM channels c
		LEFT JOIN catchup_settings cs ON cs.channel_id = c.id
		LEFT JOIN catchup_recordings cr ON cr.channel_id = c.id
		    AND cr.status IN ('recording','complete')
		WHERE c.is_active = true
		GROUP BY c.slug, c.name, cs.enabled, cs.retention_days
		ORDER BY c.name`, h.cfg.RetentionDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	type chanStatus struct {
		Slug           string  `json:"slug"`
		Name           string  `json:"name"`
		CatchupEnabled bool    `json:"catchup_enabled"`
		RetentionDays  int     `json:"retention_days"`
		RecordingHours int     `json:"recording_hours"`
		TotalMB        float64 `json:"total_mb"`
		OldestDate     *string `json:"oldest_date,omitempty"`
	}
	var channels []chanStatus
	for rows.Next() {
		var ch chanStatus
		var totalBytes int64
		var oldest sql.NullTime
		if err := rows.Scan(&ch.Slug, &ch.Name, &ch.CatchupEnabled, &ch.RetentionDays,
			&ch.RecordingHours, &totalBytes, &oldest); err != nil {
			continue
		}
		ch.TotalMB = float64(totalBytes) / (1024 * 1024)
		if oldest.Valid {
			s := oldest.Time.Format("2006-01-02")
			ch.OldestDate = &s
		}
		channels = append(channels, ch)
	}
	if channels == nil { channels = []chanStatus{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{"channels": channels})
}

func (h *handlerSet) handleCleanup(w http.ResponseWriter, r *http.Request) {
	// Internal endpoint — loopback only
	go h.archiver.cleanup(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cleanup_started"})
}

func (h *handlerSet) handleCatchupSettings(w http.ResponseWriter, r *http.Request) {
	// POST /catchup/settings/{channel_slug} — enable/disable catchup for a channel
	channelSlug := pathSegment(r.URL.Path, 2)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var input struct {
		Enabled       bool `json:"enabled"`
		RetentionDays int  `json:"retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON")
		return
	}
	if input.RetentionDays == 0 { input.RetentionDays = h.cfg.RetentionDays }

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO catchup_settings (channel_id, enabled, retention_days)
		SELECT id, $2, $3 FROM channels WHERE slug = $1
		ON CONFLICT (channel_id) DO UPDATE SET
			enabled        = EXCLUDED.enabled,
			retention_days = EXCLUDED.retention_days,
			updated_at     = NOW()`,
		channelSlug, input.Enabled, input.RetentionDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channel":        channelSlug,
		"enabled":        input.Enabled,
		"retention_days": input.RetentionDays,
	})
}

func pathSegment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n >= len(parts) { return "" }
	return parts[n]
}

// ---- main -------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	_ = os.MkdirAll(cfg.StorageDir, 0o755)

	db, err := connectDB(cfg.PostgresURL)
	if err != nil {
		log.Fatalf("[catchup] database connection failed: %v", err)
	}
	defer db.Close()

	arch := newArchiver(cfg, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start background archiver and cleanup goroutines
	go arch.run(ctx)
	go arch.runCleanup(ctx)

	hs := &handlerSet{cfg: cfg, db: db, archiver: arch}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", hs.handleHealth)
	mux.HandleFunc("GET /catchup/status", hs.handleStatus)
	mux.HandleFunc("POST /catchup/settings/", hs.handleCatchupSettings)
	mux.HandleFunc("POST /internal/catchup/cleanup", hs.handleCleanup)
	mux.HandleFunc("GET /catchup/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/playlist.m3u8") {
			hs.handleTimeRangePlaylist(w, r)
		} else {
			hs.handleChannelAvailability(w, r)
		}
	})

	addr := ":" + cfg.Port
	log.Printf("[catchup] starting on %s, storage at %s, retention %d days",
		addr, cfg.StorageDir, cfg.RetentionDays)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[catchup] server error: %v", err)
	}
}
