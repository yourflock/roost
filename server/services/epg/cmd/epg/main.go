// main.go — Roost EPG Service.
// Periodically fetches XMLTV data from configured sources, parses programs,
// and serves EPG data to Owl clients and internal services.
// Port: 8096 (env: EPG_PORT).
//
// Routes:
//   GET /health                          — service status + last sync info
//   GET /epg/status                      — per-channel program counts
//   GET /epg/xmltv?channels=c1,c2&hours=24 — XMLTV XML output (gzip supported)
//   GET /epg/json?channel_id=xxx&date=2026-02-23 — JSON program array
//   GET /epg/upcoming?channel_id=xxx&limit=5    — next N programs
//
// Internal routes (no external exposure, called by catalog service):
//   POST /internal/sync-source?id=xxx    — trigger sync for one source
package main

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"

	epgsync "github.com/unyeco/roost/services/epg/internal/sync"
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

// ---- sync state -------------------------------------------------------------

type syncState struct {
	mu           sync.RWMutex
	lastSyncAt   time.Time
	lastStatus   string
	lastError    string
	programCount int64
	syncing      atomic.Bool
}

func (s *syncState) setResult(status, errMsg string, programs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSyncAt = time.Now()
	s.lastStatus = status
	s.lastError = errMsg
	atomic.StoreInt64(&s.programCount, int64(programs))
}

// ---- server -----------------------------------------------------------------

type server struct {
	db    *sql.DB
	state *syncState
}

// ---- handlers ---------------------------------------------------------------

// GET /health
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.state.mu.RLock()
	lastSyncAt := s.state.lastSyncAt
	lastStatus := s.state.lastStatus
	lastError := s.state.lastError
	s.state.mu.RUnlock()

	programs := atomic.LoadInt64(&s.state.programCount)

	resp := map[string]interface{}{
		"status":        "ok",
		"service":       "roost-epg",
		"last_sync_at":  nil,
		"last_status":   lastStatus,
		"last_error":    lastError,
		"program_count": programs,
	}
	if !lastSyncAt.IsZero() {
		resp["last_sync_at"] = lastSyncAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /epg/status
func (s *server) handleEpgStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id, c.name, c.slug, COUNT(p.id) as program_count,
		       MIN(p.start_time) as earliest, MAX(p.end_time) as latest
		FROM channels c
		LEFT JOIN programs p ON p.channel_id = c.id AND p.end_time > now()
		WHERE c.is_active = true
		GROUP BY c.id, c.name, c.slug
		ORDER BY c.name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get EPG status")
		return
	}
	defer rows.Close()

	type chanStatus struct {
		ID           string     `json:"id"`
		Name         string     `json:"name"`
		Slug         string     `json:"slug"`
		ProgramCount int        `json:"program_count"`
		Earliest     *time.Time `json:"earliest,omitempty"`
		Latest       *time.Time `json:"latest,omitempty"`
	}
	statuses := []chanStatus{}
	for rows.Next() {
		var cs chanStatus
		var earliest, latest *time.Time
		if err := rows.Scan(&cs.ID, &cs.Name, &cs.Slug, &cs.ProgramCount, &earliest, &latest); err == nil {
			cs.Earliest = earliest
			cs.Latest = latest
			statuses = append(statuses, cs)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"channels": statuses})
}

// GET /epg/xmltv?channels=ch1,ch2&hours=24
func (s *server) handleEpgXMLTV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelFilter := q.Get("channels") // comma-separated slugs or IDs
	hours := 24
	if h := q.Get("hours"); h != "" {
		if v, err := strconv.Atoi(h); err == nil && v > 0 && v <= 168 {
			hours = v
		}
	}

	// Build channel filter
	var channelWhere string
	var args []interface{}
	args = append(args, time.Now().UTC(), time.Now().UTC().Add(time.Duration(hours)*time.Hour))

	if channelFilter != "" {
		slugs := strings.Split(channelFilter, ",")
		placeholders := make([]string, len(slugs))
		for i, slug := range slugs {
			placeholders[i] = fmt.Sprintf("$%d", i+3)
			args = append(args, strings.TrimSpace(slug))
		}
		channelWhere = fmt.Sprintf(" AND (c.slug IN (%s) OR c.id::text IN (%s))",
			strings.Join(placeholders, ","), strings.Join(placeholders, ","))
	}

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT c.id, c.name, c.slug, c.logo_url, c.epg_channel_id,
		       p.id, p.title, p.description, p.start_time, p.end_time, p.genre, p.rating
		FROM channels c
		JOIN programs p ON p.channel_id = c.id
		WHERE p.end_time >= $1 AND p.start_time <= $2
		  AND c.is_active = true%s
		ORDER BY c.slug, p.start_time`, channelWhere), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get EPG data")
		return
	}
	defer rows.Close()

	// Aggregate by channel
	type progItem struct {
		Title       string
		Desc        string
		StartTime   time.Time
		EndTime     time.Time
		Genre       string
		Rating      string
	}
	type chanItem struct {
		ID         string
		Name       string
		Slug       string
		LogoURL    *string
		EpgID      *string
		Programs   []progItem
	}

	chanMap := map[string]*chanItem{}
	chanOrder := []string{}
	for rows.Next() {
		var cID, cName, cSlug string
		var cLogoURL, cEpgID *string
		var pID, pTitle string
		var pDesc, pGenre, pRating *string
		var pStart, pEnd time.Time
		if err := rows.Scan(&cID, &cName, &cSlug, &cLogoURL, &cEpgID,
			&pID, &pTitle, &pDesc, &pStart, &pEnd, &pGenre, &pRating); err != nil {
			continue
		}
		if _, ok := chanMap[cID]; !ok {
			chanMap[cID] = &chanItem{
				ID: cID, Name: cName, Slug: cSlug, LogoURL: cLogoURL, EpgID: cEpgID,
				Programs: []progItem{},
			}
			chanOrder = append(chanOrder, cID)
		}
		prog := progItem{Title: pTitle, StartTime: pStart, EndTime: pEnd}
		if pDesc != nil {
			prog.Desc = *pDesc
		}
		if pGenre != nil {
			prog.Genre = *pGenre
		}
		if pRating != nil {
			prog.Rating = *pRating
		}
		chanMap[cID].Programs = append(chanMap[cID].Programs, prog)
	}

	// Gzip support
	var wr http.ResponseWriter = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		wr = &gzipResponseWriter{ResponseWriter: w, gz: gz}
	}

	wr.Header().Set("Content-Type", "application/xml; charset=utf-8")

	enc := xml.NewEncoder(wr)
	enc.Indent("", "  ")

	_ = enc.EncodeToken(xml.ProcInst{Target: "xml", Inst: []byte(`version="1.0" encoding="UTF-8"`)})
	_ = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "tv"}, Attr: []xml.Attr{
		{Name: xml.Name{Local: "generator-info-name"}, Value: "Roost EPG"},
	}})

	// Channels
	for _, cID := range chanOrder {
		ch := chanMap[cID]
		epgID := ch.Slug
		if ch.EpgID != nil && *ch.EpgID != "" {
			epgID = *ch.EpgID
		}
		_ = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "channel"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "id"}, Value: epgID},
		}})
		_ = enc.EncodeElement(ch.Name, xml.StartElement{Name: xml.Name{Local: "display-name"}})
		if ch.LogoURL != nil && *ch.LogoURL != "" {
			_ = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "icon"}, Attr: []xml.Attr{
				{Name: xml.Name{Local: "src"}, Value: *ch.LogoURL},
			}})
			_ = enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "icon"}})
		}
		_ = enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "channel"}})
	}

	// Programmes
	const xmltvFmt = "20060102150405 +0000"
	for _, cID := range chanOrder {
		ch := chanMap[cID]
		epgID := ch.Slug
		if ch.EpgID != nil && *ch.EpgID != "" {
			epgID = *ch.EpgID
		}
		for _, p := range ch.Programs {
			_ = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "programme"}, Attr: []xml.Attr{
				{Name: xml.Name{Local: "start"}, Value: p.StartTime.UTC().Format(xmltvFmt)},
				{Name: xml.Name{Local: "stop"}, Value: p.EndTime.UTC().Format(xmltvFmt)},
				{Name: xml.Name{Local: "channel"}, Value: epgID},
			}})
			_ = enc.EncodeElement(p.Title, xml.StartElement{Name: xml.Name{Local: "title"}})
			if p.Desc != "" {
				_ = enc.EncodeElement(p.Desc, xml.StartElement{Name: xml.Name{Local: "desc"}})
			}
			if p.Genre != "" {
				_ = enc.EncodeElement(p.Genre, xml.StartElement{Name: xml.Name{Local: "category"}})
			}
			if p.Rating != "" {
				_ = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "rating"}})
				_ = enc.EncodeElement(p.Rating, xml.StartElement{Name: xml.Name{Local: "value"}})
				_ = enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "rating"}})
			}
			_ = enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "programme"}})
		}
	}

	_ = enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "tv"}})
	_ = enc.Flush()
}

// gzipResponseWriter wraps http.ResponseWriter to write through a gzip.Writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	return g.gz.Write(b)
}

// GET /epg/json?channel_id=xxx&date=2026-02-23
func (s *server) handleEpgJSON(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID := q.Get("channel_id")
	dateStr := q.Get("date")

	if channelID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "channel_id is required")
		return
	}

	var dayStart, dayEnd time.Time
	if dateStr != "" {
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_date", "date must be YYYY-MM-DD")
			return
		}
		dayStart = d.UTC()
		dayEnd = d.UTC().Add(24 * time.Hour)
	} else {
		now := time.Now().UTC()
		dayStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		dayEnd = dayStart.Add(24 * time.Hour)
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, channel_id, title, description, start_time, end_time, genre, rating
		FROM programs
		WHERE channel_id = $1
		  AND start_time >= $2 AND end_time <= $3
		ORDER BY start_time`,
		channelID, dayStart, dayEnd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get programs")
		return
	}
	defer rows.Close()

	type programResp struct {
		ID          string     `json:"id"`
		ChannelID   string     `json:"channel_id"`
		Title       string     `json:"title"`
		Description *string    `json:"description"`
		StartTime   time.Time  `json:"start_time"`
		EndTime     time.Time  `json:"end_time"`
		Genre       *string    `json:"genre"`
		Rating      *string    `json:"rating"`
	}

	programs := []programResp{}
	for rows.Next() {
		var p programResp
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.Title, &p.Description,
			&p.StartTime, &p.EndTime, &p.Genre, &p.Rating); err == nil {
			programs = append(programs, p)
		}
	}
	writeJSON(w, http.StatusOK, programs)
}

// GET /epg/upcoming?channel_id=xxx&limit=5
func (s *server) handleEpgUpcoming(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID := q.Get("channel_id")
	limit := 5
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 20 {
			limit = v
		}
	}
	if channelID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "channel_id is required")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, channel_id, title, description, start_time, end_time, genre, rating
		FROM programs
		WHERE channel_id = $1 AND end_time > now()
		ORDER BY start_time
		LIMIT $2`,
		channelID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get upcoming programs")
		return
	}
	defer rows.Close()

	type programResp struct {
		ID          string    `json:"id"`
		ChannelID   string    `json:"channel_id"`
		Title       string    `json:"title"`
		Description *string   `json:"description"`
		StartTime   time.Time `json:"start_time"`
		EndTime     time.Time `json:"end_time"`
		Genre       *string   `json:"genre"`
		Rating      *string   `json:"rating"`
	}

	programs := []programResp{}
	for rows.Next() {
		var p programResp
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.Title, &p.Description,
			&p.StartTime, &p.EndTime, &p.Genre, &p.Rating); err == nil {
			programs = append(programs, p)
		}
	}
	writeJSON(w, http.StatusOK, programs)
}

// POST /internal/sync-source?id=xxx — trigger sync for a specific source
func (s *server) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	sourceID := r.URL.Query().Get("id")
	if sourceID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "id is required")
		return
	}

	// Lookup the source
	var src epgsync.Source
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, name, url, priority, refresh_interval_seconds FROM epg_sources WHERE id=$1`,
		sourceID).Scan(&src.ID, &src.Name, &src.URL, &src.Priority, &src.RefreshIntervalSeconds)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "EPG source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get source")
		return
	}

	// Async sync
	go func() {
		res := epgsync.SyncSource(context.Background(), s.db, src)
		if res.Error != nil {
			s.state.setResult("failed", res.Error.Error(), 0)
		} else {
			var total int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM programs`).Scan(&total)
			s.state.setResult("completed", "", total)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync_started", "source_id": sourceID})
}

// ---- scheduler --------------------------------------------------------------

func (s *server) runScheduler(ctx context.Context) {
	syncInterval := 6 * time.Hour
	retryInterval := 30 * time.Minute

	doSync := func() {
		if s.state.syncing.Swap(true) {
			log.Printf("[epg] sync already running, skipping")
			return
		}
		defer s.state.syncing.Store(false)

		log.Printf("[epg] starting scheduled sync")
		results, err := epgsync.SyncFromSources(ctx, s.db)
		if err != nil {
			log.Printf("[epg] sync sources error: %v", err)
			s.state.setResult("failed", err.Error(), 0)
			return
		}

		totalUpserted := 0
		hadError := false
		for _, r := range results {
			totalUpserted += r.ProgramsUpserted
			if r.Error != nil {
				hadError = true
				log.Printf("[epg] source %s error: %v", r.SourceID, r.Error)
			}
		}

		var totalCount int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM programs`).Scan(&totalCount)
		atomic.StoreInt64(&s.state.programCount, int64(totalCount))

		status := "completed"
		errMsg := ""
		if hadError {
			status = "partial"
			errMsg = "one or more sources failed"
		}
		s.state.setResult(status, errMsg, totalCount)
		log.Printf("[epg] sync complete: %d upserted, %d total programs", totalUpserted, totalCount)
	}

	// Sync immediately on startup
	doSync()

	for {
		s.state.mu.RLock()
		lastStatus := s.state.lastStatus
		s.state.mu.RUnlock()

		interval := syncInterval
		if lastStatus == "failed" {
			interval = retryInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			doSync()
		}
	}
}

// ---- main -------------------------------------------------------------------

func main() {
	port := getEnv("EPG_PORT", "8096")

	db, err := connectDB()
	if err != nil {
		log.Fatalf("[epg] db connect: %v", err)
	}
	defer db.Close()
	log.Printf("[epg] database connected")

	state := &syncState{lastStatus: "pending"}
	s := &server{db: db, state: state}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start background scheduler
	go s.runScheduler(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/epg/status", s.handleEpgStatus)
	mux.HandleFunc("/epg/xmltv", s.handleEpgXMLTV)
	mux.HandleFunc("/epg/json", s.handleEpgJSON)
	mux.HandleFunc("/epg/upcoming", s.handleEpgUpcoming)
	mux.HandleFunc("/internal/sync-source", s.handleSyncSource)

	log.Printf("[epg] starting on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("[epg] server error: %v", err)
	}
}
