// handler.go — HTTP handlers for podcast RSS ingestion and management.
//
// Admin routes (require superowner JWT):
//
//	POST   /admin/podcasts                             — add podcast (URL field)
//	GET    /admin/podcasts                             — list all podcasts
//	POST   /admin/podcasts/{id}/refresh               — re-fetch RSS, add new episodes
//	POST   /admin/podcasts/{id}/transcribe/{ep_id}    — trigger Whisper transcription
package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// PodcastDB wraps database access for podcast persistence.
type PodcastDB struct {
	DB *sql.DB
}

// PodcastRecord is the API representation of a stored podcast.
type PodcastRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RSSURL      string `json:"rss_url"`
	ImageURL    string `json:"image_url,omitempty"`
	EpisodeCount int   `json:"episode_count"`
	LastFetchAt string `json:"last_fetch_at,omitempty"`
}

// EpisodeRecord is the API representation of a podcast episode.
type EpisodeRecord struct {
	ID            string `json:"id"`
	PodcastID     string `json:"podcast_id"`
	GUID          string `json:"guid"`
	Title         string `json:"title"`
	AudioURL      string `json:"audio_url"`
	Duration      int    `json:"duration_secs"`
	PubDate       string `json:"pub_date"`
	HasTranscript bool   `json:"has_transcript"`
}

// Handler handles podcast admin routes.
type Handler struct {
	Store *PodcastDB
}

// NewHandler creates a Handler backed by db.
func NewHandler(db *sql.DB) *Handler {
	return &Handler{Store: &PodcastDB{DB: db}}
}

// ServeHTTP dispatches podcast admin routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/podcasts")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case path == "" && r.Method == http.MethodPost:
		h.handleAdd(w, r)
	default:
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 1 || parts[0] == "" {
			writePodcastError(w, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		podcastID := parts[0]
		if len(parts) == 2 && parts[1] == "refresh" && r.Method == http.MethodPost {
			h.handleRefresh(w, r, podcastID)
		} else if len(parts) == 3 && parts[1] == "transcribe" && r.Method == http.MethodPost {
			h.handleTranscribe(w, r, podcastID, parts[2])
		} else {
			writePodcastError(w, http.StatusNotFound, "not_found", "endpoint not found")
		}
	}
}

// handleAdd handles POST /admin/podcasts.
// Body: { "url": "https://..." }
func (h *Handler) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writePodcastError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}
	if !strings.HasPrefix(req.URL, "http") {
		writePodcastError(w, http.StatusBadRequest, "invalid_url", "url must be http(s)")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	feed, err := FetchPodcast(ctx, req.URL)
	if err != nil {
		writePodcastError(w, http.StatusBadRequest, "fetch_error", err.Error())
		return
	}

	id, episodeCount, err := h.Store.insertPodcast(r.Context(), req.URL, feed)
	if err != nil {
		log.Printf("[podcasts] insert podcast: %v", err)
		writePodcastError(w, http.StatusInternalServerError, "db_error", "failed to store podcast")
		return
	}

	writePodcastJSON(w, http.StatusCreated, map[string]interface{}{
		"id":            id,
		"title":         feed.Title,
		"episode_count": episodeCount,
	})
}

// handleList handles GET /admin/podcasts.
func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Store.DB.QueryContext(r.Context(), `
		SELECT id, title, COALESCE(description,''), rss_url, COALESCE(image_url,''),
		       episode_count, COALESCE(last_fetch_at::text,'')
		FROM podcasts
		ORDER BY created_at DESC
		LIMIT 200
	`)
	if err != nil {
		writePodcastError(w, http.StatusInternalServerError, "db_error", "query failed")
		return
	}
	defer rows.Close()

	var podcasts []PodcastRecord
	for rows.Next() {
		var p PodcastRecord
		if err := rows.Scan(&p.ID, &p.Title, &p.Description, &p.RSSURL,
			&p.ImageURL, &p.EpisodeCount, &p.LastFetchAt); err != nil {
			continue
		}
		podcasts = append(podcasts, p)
	}
	if podcasts == nil {
		podcasts = []PodcastRecord{}
	}
	writePodcastJSON(w, http.StatusOK, podcasts)
}

// handleRefresh handles POST /admin/podcasts/{id}/refresh.
// Re-fetches the RSS feed and inserts any new episodes not already in the DB.
func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request, podcastID string) {
	var rssURL string
	err := h.Store.DB.QueryRowContext(r.Context(),
		`SELECT rss_url FROM podcasts WHERE id = $1`, podcastID).Scan(&rssURL)
	if err == sql.ErrNoRows {
		writePodcastError(w, http.StatusNotFound, "not_found", "podcast not found")
		return
	}
	if err != nil {
		writePodcastError(w, http.StatusInternalServerError, "db_error", "lookup failed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	feed, err := FetchPodcast(ctx, rssURL)
	if err != nil {
		writePodcastError(w, http.StatusBadRequest, "fetch_error", err.Error())
		return
	}

	// Get existing GUIDs to find new episodes.
	existing, err := h.Store.existingGUIDs(r.Context(), podcastID)
	if err != nil {
		writePodcastError(w, http.StatusInternalServerError, "db_error", "guid lookup failed")
		return
	}

	newEps := NewEpisodes(feed, existing)
	if len(newEps) == 0 {
		writePodcastJSON(w, http.StatusOK, map[string]interface{}{
			"added": 0,
			"message": "no new episodes",
		})
		return
	}

	added := 0
	for _, ep := range newEps {
		if err := h.Store.insertEpisode(r.Context(), podcastID, ep); err != nil {
			log.Printf("[podcasts] insert episode %q: %v", ep.Title, err)
		} else {
			added++
		}
	}

	_, _ = h.Store.DB.ExecContext(r.Context(), `
		UPDATE podcasts
		SET episode_count = episode_count + $1, last_fetch_at = NOW()
		WHERE id = $2
	`, added, podcastID)

	writePodcastJSON(w, http.StatusOK, map[string]interface{}{
		"added": added,
	})
}

// handleTranscribe handles POST /admin/podcasts/{id}/transcribe/{episode_id}.
// Triggers async Whisper transcription for a single episode.
func (h *Handler) handleTranscribe(w http.ResponseWriter, r *http.Request, podcastID, episodeID string) {
	var audioURL string
	err := h.Store.DB.QueryRowContext(r.Context(), `
		SELECT audio_url FROM podcast_episodes
		WHERE id = $1 AND podcast_id = $2
	`, episodeID, podcastID).Scan(&audioURL)
	if err == sql.ErrNoRows {
		writePodcastError(w, http.StatusNotFound, "not_found", "episode not found")
		return
	}
	if err != nil {
		writePodcastError(w, http.StatusInternalServerError, "db_error", "lookup failed")
		return
	}

	// Run transcription asynchronously.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
		defer cancel()

		model := getEnv("WHISPER_MODEL", "base")
		result, err := TranscribeWithModel(ctx, audioURL, model)
		if err != nil {
			log.Printf("[podcasts] transcribe episode %s: %v", episodeID, err)
			_, _ = h.Store.DB.ExecContext(context.Background(), `
				UPDATE podcast_episodes SET transcript_status = 'failed' WHERE id = $1
			`, episodeID)
			return
		}

		_, _ = h.Store.DB.ExecContext(context.Background(), `
			UPDATE podcast_episodes
			SET transcript_vtt = $1, transcript_lang = $2, transcript_status = 'done', updated_at = NOW()
			WHERE id = $3
		`, result.VTT, result.Language, episodeID)
		log.Printf("[podcasts] transcription done: episode %s (lang=%s)", episodeID, result.Language)
	}()

	writePodcastJSON(w, http.StatusAccepted, map[string]string{
		"status":  "queued",
		"episode": episodeID,
	})
}

// ── DB helpers ────────────────────────────────────────────────────────────────

func (db *PodcastDB) insertPodcast(ctx context.Context, rssURL string, feed *PodcastFeed) (string, int, error) {
	var id string
	err := db.DB.QueryRowContext(ctx, `
		INSERT INTO podcasts (title, description, rss_url, image_url, episode_count, last_fetch_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		RETURNING id
	`, feed.Title, feed.Description, rssURL, nullS(feed.ImageURL), len(feed.Episodes)).Scan(&id)
	if err != nil {
		return "", 0, err
	}

	for _, ep := range feed.Episodes {
		_ = db.insertEpisode(ctx, id, ep)
	}
	return id, len(feed.Episodes), nil
}

func (db *PodcastDB) insertEpisode(ctx context.Context, podcastID string, ep Episode) error {
	dur := ParseEpisodeDuration(ep.Duration)
	_, err := db.DB.ExecContext(ctx, `
		INSERT INTO podcast_episodes (podcast_id, guid, title, audio_url, duration_secs, pub_date, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (podcast_id, guid) DO NOTHING
	`, podcastID, ep.GUID, ep.Title, ep.Enclosure.URL, dur, nullS(ep.PubDate), nullS(ep.Description))
	return err
}

func (db *PodcastDB) existingGUIDs(ctx context.Context, podcastID string) (map[string]bool, error) {
	rows, err := db.DB.QueryContext(ctx,
		`SELECT guid FROM podcast_episodes WHERE podcast_id = $1`, podcastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	guids := make(map[string]bool)
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err == nil {
			guids[g] = true
		}
	}
	return guids, rows.Err()
}

// ── Response helpers ──────────────────────────────────────────────────────────

func writePodcastJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writePodcastError(w http.ResponseWriter, status int, code, msg string) {
	writePodcastJSON(w, status, map[string]string{"error": code, "message": msg})
}

func nullS(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ensure fmt is used
var _ = fmt.Sprintf
