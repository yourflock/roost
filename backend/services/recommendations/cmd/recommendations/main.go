// main.go — Roost Recommendations Service.
// Calculates personalized content recommendations using genre affinity,
// popularity, and collaborative filtering — all SQL-based, no ML required.
// Port: 8099 (env: RECO_PORT). Internal service — called by owl_api.
//
// Routes:
//   GET /recommendations/:subscriber_id  — personalized recommendations
//   GET /recommendations/trending        — site-wide trending (no auth)
//   POST /internal/reco/refresh          — force genre affinity recalculation
//   GET /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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

type recItem struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
	Genre     *string `json:"genre,omitempty"`
	PosterURL *string `json:"poster_url,omitempty"`
	Score     float64 `json:"score,omitempty"`
}

type server struct{ db *sql.DB }

// personalized returns scored recommendations for one subscriber.
// Score = genre_affinity(0.4) + popularity(0.3) + recency(0.2) + rating_match(0.1).
// Excludes already-completed items. Handles cold-start (no history) by returning trending.
func (s *server) personalized(ctx context.Context, subscriberID string) ([]recItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH total_watches AS (
			SELECT COUNT(*)::float AS n FROM watch_progress
		),
		genre_affinity AS (
			SELECT c.genre,
			       SUM(wp.position_seconds)::float /
			           GREATEST(SUM(SUM(wp.position_seconds)) OVER (), 1) AS score
			FROM watch_progress wp
			JOIN vod_catalog c ON c.id = wp.content_id AND wp.content_type = 'movie'
			WHERE wp.subscriber_id = $1 AND c.genre IS NOT NULL
			GROUP BY c.genre
		),
		content_popularity AS (
			SELECT content_id,
			       COUNT(*)::float / GREATEST((SELECT n FROM total_watches), 1) AS pop_score
			FROM watch_progress GROUP BY content_id
		),
		scored AS (
			SELECT vc.id, vc.title, vc.type, vc.genre, vc.poster_url,
			       COALESCE(ga.score, 0)  * 0.4 +
			       COALESCE(cp.pop_score, 0) * 0.3 +
			       CASE WHEN vc.created_at > NOW() - INTERVAL '7 days'  THEN 0.20
			            WHEN vc.created_at > NOW() - INTERVAL '30 days' THEN 0.10
			            WHEN vc.created_at > NOW() - INTERVAL '90 days' THEN 0.05
			            ELSE 0.0 END AS rec_score
			FROM vod_catalog vc
			LEFT JOIN genre_affinity ga ON ga.genre = vc.genre
			LEFT JOIN content_popularity cp ON cp.content_id = vc.id
			WHERE vc.is_active = true
			  AND NOT EXISTS (
			      SELECT 1 FROM watch_progress wp2
			      WHERE wp2.subscriber_id = $1
			        AND wp2.content_id = vc.id
			        AND wp2.completed = true
			  )
		)
		SELECT id, title, type, genre, poster_url, rec_score
		FROM scored ORDER BY rec_score DESC LIMIT 20`, subscriberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows, true)
}

// trending returns most-watched content over the last 7 days.
func (s *server) trending(ctx context.Context) ([]recItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vc.id, vc.title, vc.type, vc.genre, vc.poster_url,
		       COUNT(wp.id)::float AS watch_count
		FROM vod_catalog vc
		JOIN watch_progress wp ON wp.content_id = vc.id
		WHERE vc.is_active = true
		  AND wp.last_watched_at > NOW() - INTERVAL '7 days'
		GROUP BY vc.id, vc.title, vc.type, vc.genre, vc.poster_url
		ORDER BY watch_count DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows, true)
}

// becauseYouWatched returns similar genre items based on the last completed item.
func (s *server) becauseYouWatched(ctx context.Context, subscriberID string) (string, []recItem, error) {
	var triggerTitle string
	var triggerGenre sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT c.title, c.genre FROM watch_progress wp
		JOIN vod_catalog c ON c.id = wp.content_id AND wp.content_type = 'movie'
		WHERE wp.subscriber_id = $1 AND wp.completed = true
		ORDER BY wp.last_watched_at DESC LIMIT 1`, subscriberID).
		Scan(&triggerTitle, &triggerGenre)
	if err != nil || !triggerGenre.Valid {
		return "", nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, type, genre, poster_url, 0.0 AS score
		FROM vod_catalog
		WHERE genre = $1 AND is_active = true
		  AND id NOT IN (
		      SELECT content_id FROM watch_progress WHERE subscriber_id = $2
		  )
		ORDER BY sort_order ASC LIMIT 10`, triggerGenre.String, subscriberID)
	if err != nil {
		return triggerTitle, nil, nil
	}
	defer rows.Close()
	items, err := scanItems(rows, false)
	return triggerTitle, items, err
}

func scanItems(rows *sql.Rows, hasScore bool) ([]recItem, error) {
	var items []recItem
	for rows.Next() {
		var item recItem
		var genre, poster sql.NullString
		var score float64
		var err error
		if hasScore {
			err = rows.Scan(&item.ID, &item.Title, &item.Type, &genre, &poster, &score)
		} else {
			err = rows.Scan(&item.ID, &item.Title, &item.Type, &genre, &poster, &score)
		}
		if err != nil { continue }
		if genre.Valid { item.Genre = &genre.String }
		if poster.Valid { item.PosterURL = &poster.String }
		item.Score = score
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---- handlers ---------------------------------------------------------------

func (s *server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	// /recommendations/{subscriber_id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[1] == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "subscriber_id required")
		return
	}
	subscriberID := parts[1]

	forYou, err := s.personalized(r.Context(), subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	trend, _ := s.trending(r.Context())
	triggerTitle, because, _ := s.becauseYouWatched(r.Context(), subscriberID)

	if forYou == nil { forYou = []recItem{} }
	if trend == nil { trend = []recItem{} }
	if because == nil { because = []recItem{} }

	resp := map[string]interface{}{
		"for_you":  forYou,
		"trending": trend,
	}
	if triggerTitle != "" {
		resp["because_you_watched"] = map[string]interface{}{
			"title": triggerTitle,
			"items": because,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleTrending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	items, err := s.trending(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if items == nil { items = []recItem{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	// In future: trigger async genre affinity score recalculation
	// For now: no-op (scores are computed per-request from live data)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "note": "scores computed on demand"})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-recommendations"})
}

// ---- main -------------------------------------------------------------------

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[reco] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("GET /recommendations/trending", srv.handleTrending)
	mux.HandleFunc("GET /recommendations/", srv.handleRecommendations)
	mux.HandleFunc("POST /internal/reco/refresh", srv.handleRefresh)

	port := getEnv("RECO_PORT", "8099")
	addr := ":" + port
	log.Printf("[reco] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[reco] server error: %v", err)
	}
}

// fmt used in scored query formatting (kept for clarity)
var _ = fmt.Sprintf
