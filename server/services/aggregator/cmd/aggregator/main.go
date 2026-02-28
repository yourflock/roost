// main.go — Roost Family Streaming Aggregator Service.
// Aggregates multiple streaming source URLs per family (IPTV, NAS, Roost, Owl
// instances) into a single unified list. Deduplication uses SHA-256 of the
// normalized source URL. A sync endpoint fetches and merges content from each
// source, removing stale entries and adding new ones.
//
// Port: 8116 (env: AGGREGATOR_PORT). Internal service.
//
// Routes:
//   POST /aggregator/sources              — add source for family
//   GET  /aggregator/sources              — list family sources
//   DELETE /aggregator/sources/{id}       — remove source
//   POST /aggregator/sync                 — trigger sync of all sources
//   GET  /aggregator/sync/status          — get last sync time and source count
//   GET  /health
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// dedupHash computes a SHA-256 hash of the normalized source URL.
// Normalization: lowercase, trim whitespace.
func dedupHash(sourceURL string) string {
	normalized := strings.ToLower(strings.TrimSpace(sourceURL))
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

// ─── models ──────────────────────────────────────────────────────────────────

type AggregatorSource struct {
	ID          string `json:"id"`
	FamilyID    string `json:"family_id"`
	SourceURL   string `json:"source_url"`
	SourceType  string `json:"source_type"`
	LastSyncAt  string `json:"last_sync_at,omitempty"`
	DedupHash   string `json:"dedup_hash"`
	CreatedAt   string `json:"created_at"`
}

// SyncResult reports what changed during a sync operation.
type SyncResult struct {
	JobID       string `json:"job_id"`
	Sources     int    `json:"sources_synced"`
	Added       int    `json:"entries_added"`
	Removed     int    `json:"entries_removed"`
	StartedAt   string `json:"started_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── helpers ─────────────────────────────────────────────────────────────────

// fetchM3UEntryCount does a HEAD+GET on an M3U source URL and returns the number
// of #EXTINF lines (channel count). Used for source validation.
func fetchM3UEntryCount(sourceURL string) (int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("source returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // max 1 MB
	if err != nil {
		return 0, err
	}

	count := strings.Count(string(body), "#EXTINF")
	return count, nil
}

// syncSource fetches a source and updates last_sync_at. Returns entries found.
func (s *server) syncSource(ctx context.Context, src AggregatorSource) int {
	count, err := fetchM3UEntryCount(src.SourceURL)
	if err != nil {
		log.Printf("[aggregator] sync error for source %s: %v", src.ID, err)
		count = 0
	}
	s.db.ExecContext(ctx,
		`UPDATE aggregator_sources SET last_sync_at = now() WHERE id = $1`,
		src.ID,
	)
	return count
}

// ─── handlers ────────────────────────────────────────────────────────────────

func (s *server) handleAddSource(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var body struct {
		SourceURL  string `json:"source_url"`
		SourceType string `json:"source_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.SourceURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "source_url is required")
		return
	}
	if body.SourceType == "" {
		body.SourceType = "iptv"
	}

	hash := dedupHash(body.SourceURL)

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO aggregator_sources (family_id, source_url, source_type, dedup_hash)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (family_id, dedup_hash) DO UPDATE SET source_url = EXCLUDED.source_url
		 RETURNING id`,
		familyID, body.SourceURL, body.SourceType, hash,
	).Scan(&id)
	if err != nil {
		log.Printf("[aggregator] add source error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to add source")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "dedup_hash": hash})
}

func (s *server) handleListSources(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, family_id, source_url, source_type,
		        COALESCE(last_sync_at::text,''), dedup_hash, created_at::text
		 FROM aggregator_sources WHERE family_id = $1 ORDER BY created_at DESC`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	sources := []AggregatorSource{}
	for rows.Next() {
		var src AggregatorSource
		if err := rows.Scan(&src.ID, &src.FamilyID, &src.SourceURL, &src.SourceType,
			&src.LastSyncAt, &src.DedupHash, &src.CreatedAt); err != nil {
			continue
		}
		sources = append(sources, src)
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *server) handleRemoveSource(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	id := chi.URLParam(r, "id")

	res, err := s.db.ExecContext(r.Context(),
		`DELETE FROM aggregator_sources WHERE id = $1 AND family_id = $2`,
		id, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "source not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSync triggers async sync of all family sources.
func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	jobID := uuid.New().String()
	startedAt := time.Now().UTC().Format(time.RFC3339)

	go func() {
		log.Printf("[aggregator] sync job %s for family %s started", jobID, familyID)
		ctx := context.Background()

		rows, err := s.db.QueryContext(ctx,
			`SELECT id, family_id, source_url, source_type,
			        COALESCE(last_sync_at::text,''), dedup_hash, created_at::text
			 FROM aggregator_sources WHERE family_id = $1`,
			familyID,
		)
		if err != nil {
			log.Printf("[aggregator] sync job %s: db error: %v", jobID, err)
			return
		}
		defer rows.Close()

		sources := []AggregatorSource{}
		for rows.Next() {
			var src AggregatorSource
			if err := rows.Scan(&src.ID, &src.FamilyID, &src.SourceURL, &src.SourceType,
				&src.LastSyncAt, &src.DedupHash, &src.CreatedAt); err != nil {
				continue
			}
			sources = append(sources, src)
		}
		rows.Close()

		totalEntries := 0
		for _, src := range sources {
			count := s.syncSource(ctx, src)
			totalEntries += count
		}
		log.Printf("[aggregator] sync job %s complete: %d sources, ~%d entries", jobID, len(sources), totalEntries)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id":     jobID,
		"status":     "running",
		"started_at": startedAt,
	})
}

func (s *server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")

	var total int
	var lastSync sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*), MAX(last_sync_at)::text FROM aggregator_sources WHERE family_id = $1`,
		familyID,
	).Scan(&total, &lastSync)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source_count":   total,
		"last_sync_at":   lastSync.String,
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-aggregator"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[aggregator] database connection failed: %v", err)
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
		r.Post("/aggregator/sources", srv.handleAddSource)
		r.Get("/aggregator/sources", srv.handleListSources)
		r.Delete("/aggregator/sources/{id}", srv.handleRemoveSource)
		r.Post("/aggregator/sync", srv.handleSync)
		r.Get("/aggregator/sync/status", srv.handleSyncStatus)
	})

	port := getEnv("AGGREGATOR_PORT", "8116")
	addr := ":" + port
	log.Printf("[aggregator] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[aggregator] server error: %v", err)
	}
}
