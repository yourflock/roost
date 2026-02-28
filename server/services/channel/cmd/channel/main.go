// main.go — Roost Family Channel Playlist Service.
// Manages family-curated playlists that blend any content type (VOD, live,
// podcast, game) into a sequential, shuffled, or round-robin schedule.
// Generates M3U8-compatible playlists for Owl and produces a schedule endpoint
// for the EPG grid compositor.
//
// Port: 8112 (env: CHANNEL_PORT). Internal service and owl_api.
//
// Routes:
//   POST /channel/playlists              — create playlist
//   GET  /channel/playlists              — list family playlists
//   GET  /channel/playlists/{id}         — get playlist with items
//   PUT  /channel/playlists/{id}         — update playlist (name, items, schedule_type)
//   DELETE /channel/playlists/{id}       — delete playlist
//   GET  /channel/playlists/{id}/m3u8    — generate M3U8 playlist file
//   GET  /channel/playlists/{id}/schedule — get next-N-hours EPG schedule
//   GET  /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
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

// PlaylistItem represents one entry in a channel playlist.
type PlaylistItem struct {
	ContentID   string `json:"content_id"`
	ContentType string `json:"content_type"` // movie | show_episode | live | podcast | game
	Title       string `json:"title"`
	DurationSec int    `json:"duration_sec"`
	StreamURL   string `json:"stream_url,omitempty"`
}

type Playlist struct {
	ID           string         `json:"id"`
	FamilyID     string         `json:"family_id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Items        []PlaylistItem `json:"items"`
	ScheduleType string         `json:"schedule_type"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── helpers ─────────────────────────────────────────────────────────────────

func parseItems(raw []byte) ([]PlaylistItem, error) {
	var items []PlaylistItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func marshalItems(items []PlaylistItem) ([]byte, error) {
	return json.Marshal(items)
}

// applyScheduleType reorders items according to schedule_type.
// sequential: as-stored, shuffle: random, round_robin: interleave by content_type.
func applyScheduleType(items []PlaylistItem, scheduleType string) []PlaylistItem {
	if len(items) == 0 {
		return items
	}
	switch scheduleType {
	case "shuffle":
		out := make([]PlaylistItem, len(items))
		copy(out, items)
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	case "round_robin":
		// Group by content_type, then interleave.
		byType := map[string][]PlaylistItem{}
		order := []string{}
		for _, item := range items {
			if _, seen := byType[item.ContentType]; !seen {
				order = append(order, item.ContentType)
			}
			byType[item.ContentType] = append(byType[item.ContentType], item)
		}
		out := make([]PlaylistItem, 0, len(items))
		maxLen := 0
		for _, t := range order {
			if len(byType[t]) > maxLen {
				maxLen = len(byType[t])
			}
		}
		for i := 0; i < maxLen; i++ {
			for _, t := range order {
				if i < len(byType[t]) {
					out = append(out, byType[t][i])
				}
			}
		}
		return out
	default:
		return items
	}
}

// ─── handlers ────────────────────────────────────────────────────────────────

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		Items        []PlaylistItem `json:"items"`
		ScheduleType string         `json:"schedule_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if body.ScheduleType == "" {
		body.ScheduleType = "sequential"
	}
	if body.Items == nil {
		body.Items = []PlaylistItem{}
	}

	itemsJSON, err := marshalItems(body.Items)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid items")
		return
	}

	var id string
	err = s.db.QueryRowContext(r.Context(),
		`INSERT INTO channel_playlists (family_id, name, description, items, schedule_type)
		 VALUES ($1, $2, $3, $4::jsonb, $5) RETURNING id`,
		familyID, body.Name, body.Description, string(itemsJSON), body.ScheduleType,
	).Scan(&id)
	if err != nil {
		log.Printf("[channel] create error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create playlist")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, family_id, name, COALESCE(description,''), items, schedule_type,
		        created_at::text, updated_at::text
		 FROM channel_playlists WHERE family_id = $1 ORDER BY name ASC`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	playlists := []Playlist{}
	for rows.Next() {
		var p Playlist
		var itemsRaw []byte
		if err := rows.Scan(&p.ID, &p.FamilyID, &p.Name, &p.Description,
			&itemsRaw, &p.ScheduleType, &p.CreatedAt, &p.UpdatedAt); err != nil {
			continue
		}
		items, _ := parseItems(itemsRaw)
		if items == nil {
			items = []PlaylistItem{}
		}
		p.Items = items
		playlists = append(playlists, p)
	}
	writeJSON(w, http.StatusOK, playlists)
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var p Playlist
	var itemsRaw []byte
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, family_id, name, COALESCE(description,''), items, schedule_type,
		        created_at::text, updated_at::text
		 FROM channel_playlists WHERE id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(&p.ID, &p.FamilyID, &p.Name, &p.Description,
		&itemsRaw, &p.ScheduleType, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "playlist not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items, _ := parseItems(itemsRaw)
	if items == nil {
		items = []PlaylistItem{}
	}
	p.Items = items
	writeJSON(w, http.StatusOK, p)
}

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var body struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		Items        []PlaylistItem `json:"items"`
		ScheduleType string         `json:"schedule_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	itemsJSON, err := marshalItems(body.Items)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid items")
		return
	}

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE channel_playlists
		 SET name = COALESCE(NULLIF($1,''), name),
		     description = $2,
		     items = $3::jsonb,
		     schedule_type = COALESCE(NULLIF($4,''), schedule_type),
		     updated_at = now()
		 WHERE id = $5 AND family_id = $6`,
		body.Name, body.Description, string(itemsJSON), body.ScheduleType, id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "playlist not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM channel_playlists WHERE id = $1 AND family_id = $2`,
		id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "playlist not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleM3U8 returns an M3U8 Extended playlist for Owl or any HLS-compatible player.
func (s *server) handleM3U8(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var p Playlist
	var itemsRaw []byte
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, name, items, schedule_type FROM channel_playlists WHERE id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(&p.ID, &p.Name, &itemsRaw, &p.ScheduleType)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "playlist not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	items, _ := parseItems(itemsRaw)
	items = applyScheduleType(items, p.ScheduleType)

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString(fmt.Sprintf("#PLAYLIST:%s\n", p.Name))
	for _, item := range items {
		dur := item.DurationSec
		if dur == 0 {
			dur = -1
		}
		sb.WriteString(fmt.Sprintf("#EXTINF:%d,%s\n", dur, item.Title))
		url := item.StreamURL
		if url == "" {
			url = fmt.Sprintf("%s/stream/%s/%s",
				getEnv("ROOST_API_BASE", "https://roost.unity.dev"),
				item.ContentType, item.ContentID,
			)
		}
		sb.WriteString(url + "\n")
	}

	w.Header().Set("Content-Type", "application/x-mpegurl")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.m3u8"`, uuid.New().String()))
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, sb.String())
}

// ScheduleEntry represents a time-slotted item in an EPG schedule.
type ScheduleEntry struct {
	ContentID   string `json:"content_id"`
	ContentType string `json:"content_type"`
	Title       string `json:"title"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	DurationSec int    `json:"duration_sec"`
}

// handleSchedule produces a 6-hour forward-looking EPG schedule from the playlist.
func (s *server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	var itemsRaw []byte
	var scheduleType string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT items, schedule_type FROM channel_playlists WHERE id = $1 AND family_id = $2`,
		id, familyID,
	).Scan(&itemsRaw, &scheduleType)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "playlist not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	items, _ := parseItems(itemsRaw)
	items = applyScheduleType(items, scheduleType)
	if len(items) == 0 {
		writeJSON(w, http.StatusOK, []ScheduleEntry{})
		return
	}

	now := time.Now().UTC()
	limit := now.Add(6 * time.Hour)
	cursor := now
	entries := []ScheduleEntry{}
	i := 0

	for cursor.Before(limit) && len(entries) < 100 {
		item := items[i%len(items)]
		dur := item.DurationSec
		if dur <= 0 {
			dur = 1800 // default 30 min for items without duration
		}
		end := cursor.Add(time.Duration(dur) * time.Second)
		entries = append(entries, ScheduleEntry{
			ContentID:   item.ContentID,
			ContentType: item.ContentType,
			Title:       item.Title,
			StartTime:   cursor.Format(time.RFC3339),
			EndTime:     end.Format(time.RFC3339),
			DurationSec: dur,
		})
		cursor = end
		i++
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-channel"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[channel] database connection failed: %v", err)
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
		r.Post("/channel/playlists", srv.handleCreate)
		r.Get("/channel/playlists", srv.handleList)
		r.Get("/channel/playlists/{id}", srv.handleGet)
		r.Put("/channel/playlists/{id}", srv.handleUpdate)
		r.Delete("/channel/playlists/{id}", srv.handleDelete)
		r.Get("/channel/playlists/{id}/m3u8", srv.handleM3U8)
		r.Get("/channel/playlists/{id}/schedule", srv.handleSchedule)
	})

	port := getEnv("CHANNEL_PORT", "8112")
	addr := ":" + port
	log.Printf("[channel] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[channel] server error: %v", err)
	}
}
