// subtitles.go — OpenSubtitles REST API v1 integration for catalog service.
// Fetches and caches subtitle files for VOD content.
// Cache table: subtitle_files (content_id, language, r2_key, score, fetched_at)
// TTL: 24 hours. Stale entries refreshed by SubtitleRefreshJob.
//
// Env vars:
//   OPENSUBTITLES_API_KEY — required for subtitle lookups
//   R2_ENDPOINT           — Cloudflare R2 endpoint for subtitle storage
package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	openSubtitlesBase = "https://rest.opensubtitles.com/api/v1"
	subtitleTTL       = 24 * time.Hour
	subtitleBatchSize = 50
)

// osSearchResult is the top-level response from the OpenSubtitles search endpoint.
type osSearchResult struct {
	Data []struct {
		ID         int `json:"id"`
		Attributes struct {
			Language      string  `json:"language"`
			DownloadCount int     `json:"download_count"`
			SubFormat     string  `json:"sub_format"` // "srt", "webvtt", etc.
			URL           string  `json:"url"`
			Ratings       float64 `json:"ratings"`
			FileID        int     `json:"file_id"`
		} `json:"attributes"`
	} `json:"data"`
}

// osDownloadResponse is returned by POST /download.
type osDownloadResponse struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
	Message  string `json:"message"`
}

func osAPIKey() string {
	return os.Getenv("OPENSUBTITLES_API_KEY")
}

func osClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// osSearch calls the OpenSubtitles search API.
// imdbID should be in the format "tt1234567" (with the tt prefix).
func osSearch(imdbID, lang string) (*osSearchResult, error) {
	apiKey := osAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("OPENSUBTITLES_API_KEY not set")
	}

	q := url.Values{}
	q.Set("imdb_id", strings.TrimPrefix(imdbID, "tt"))
	q.Set("languages", lang)

	reqURL := openSubtitlesBase + "/subtitles?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Roost/1.0")

	resp, err := osClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("opensubtitles rate limit exceeded")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opensubtitles search HTTP %d: %s", resp.StatusCode, body)
	}

	var result osSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode opensubtitles response: %w", err)
	}
	return &result, nil
}

// osGetDownloadLink fetches the signed download link for a subtitle file.
func osGetDownloadLink(fileID int) (string, error) {
	apiKey := osAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("OPENSUBTITLES_API_KEY not set")
	}

	body := fmt.Sprintf(`{"file_id":%d}`, fileID)
	req, err := http.NewRequest(http.MethodPost,
		openSubtitlesBase+"/download",
		strings.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Roost/1.0")

	resp, err := osClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("opensubtitles download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("opensubtitles download HTTP %d: %s", resp.StatusCode, b)
	}

	var dl osDownloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&dl); err != nil {
		return "", err
	}
	if dl.Link == "" {
		return "", fmt.Errorf("opensubtitles returned empty link: %s", dl.Message)
	}
	return dl.Link, nil
}

// FetchSubtitles retrieves and caches subtitle metadata for a piece of content.
// contentID is the Roost canonical ID (e.g. "imdb:tt1375666").
// imdbID is the raw IMDB ID (e.g. "tt1375666").
// lang is the BCP-47 language code (e.g. "en", "ar", "fr").
//
// If a fresh (< 24h) cache entry exists, it returns immediately.
// Otherwise it calls the OpenSubtitles API and upserts the result.
func FetchSubtitles(db *sql.DB, contentID, imdbID, lang string) error {
	// Check cache freshness
	var fetchedAt time.Time
	err := db.QueryRow(
		`SELECT fetched_at FROM subtitle_files WHERE content_id = $1 AND language = $2`,
		contentID, lang,
	).Scan(&fetchedAt)
	if err == nil && time.Since(fetchedAt) < subtitleTTL {
		return nil // cache is fresh
	}

	result, err := osSearch(imdbID, lang)
	if err != nil {
		return err
	}
	if len(result.Data) == 0 {
		// No subtitles found — upsert a null record so we don't spam the API
		_, err = db.Exec(
			`INSERT INTO subtitle_files (content_id, language, r2_key, score, fetched_at)
			 VALUES ($1, $2, NULL, 0, NOW())
			 ON CONFLICT (content_id, language) DO UPDATE
			 SET fetched_at = NOW(), score = 0`,
			contentID, lang,
		)
		return err
	}

	// Pick the highest-rated result
	best := result.Data[0]
	for _, d := range result.Data[1:] {
		if d.Attributes.DownloadCount > best.Attributes.DownloadCount {
			best = d
		}
	}

	// Get the signed download link
	downloadLink, err := osGetDownloadLink(best.Attributes.FileID)
	if err != nil {
		return err
	}

	// Normalise score to [0,1] range using download count as proxy
	score := 0.5
	if best.Attributes.Ratings > 0 {
		score = best.Attributes.Ratings / 10.0
		if score > 1 {
			score = 1
		}
	}

	_, err = db.Exec(
		`INSERT INTO subtitle_files (content_id, language, r2_key, score, fetched_at)
		 VALUES ($1, $2, $3, $4, NOW())
		 ON CONFLICT (content_id, language) DO UPDATE
		 SET r2_key = $3, score = $4, fetched_at = NOW()`,
		contentID, lang, downloadLink, score,
	)
	return err
}

// HandleGetSubtitles serves cached subtitle metadata for a content item.
// GET /v1/content/{id}/subtitles?lang=en
//
// Returns 200 with subtitle record when cached.
// Returns 202 Accepted and triggers a background fetch when cache misses.
func HandleGetSubtitles(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentID := r.PathValue("id")
		lang := r.URL.Query().Get("lang")
		if lang == "" {
			lang = "en"
		}

		type SubtitleRecord struct {
			ContentID string  `json:"content_id"`
			Language  string  `json:"language"`
			R2Key     *string `json:"r2_key"`
			Score     float64 `json:"score"`
			FetchedAt time.Time `json:"fetched_at"`
		}

		var rec SubtitleRecord
		var r2Key sql.NullString
		err := db.QueryRowContext(r.Context(),
			`SELECT content_id, language, r2_key, score, fetched_at
			 FROM subtitle_files WHERE content_id = $1 AND language = $2`,
			contentID, lang,
		).Scan(&rec.ContentID, &rec.Language, &r2Key, &rec.Score, &rec.FetchedAt)

		if err == sql.ErrNoRows {
			// Cache miss — trigger background fetch and return 202
			go func() {
				// Attempt to derive IMDB ID from content_id prefix (e.g. "imdb:tt1375666")
				imdbID := ""
				if strings.HasPrefix(contentID, "imdb:") {
					imdbID = strings.TrimPrefix(contentID, "imdb:")
				}
				if imdbID != "" {
					_ = FetchSubtitles(db, contentID, imdbID, lang)
				}
			}()
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "fetching",
				"message": "Subtitle fetch initiated. Retry in a few seconds.",
			})
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		if r2Key.Valid {
			rec.R2Key = &r2Key.String
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	}
}

// SubtitleRefreshJob refreshes stale subtitle cache entries.
// Intended to be called from a cron goroutine (e.g. every 6 hours).
// Processes up to subtitleBatchSize content items per run.
func SubtitleRefreshJob(db *sql.DB) {
	rows, err := db.Query(
		`SELECT content_id, language FROM subtitle_files
		 WHERE fetched_at < NOW() - INTERVAL '24 hours'
		 LIMIT $1`,
		subtitleBatchSize,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type entry struct {
		contentID string
		lang      string
	}
	var stale []entry
	for rows.Next() {
		var e entry
		if rows.Scan(&e.contentID, &e.lang) == nil {
			stale = append(stale, e)
		}
	}

	for _, e := range stale {
		imdbID := ""
		if strings.HasPrefix(e.contentID, "imdb:") {
			imdbID = strings.TrimPrefix(e.contentID, "imdb:")
		}
		if imdbID == "" {
			continue // can't refresh without an IMDB ID
		}
		// Best-effort; errors are silently ignored in the refresh job
		_ = FetchSubtitles(db, e.contentID, imdbID, e.lang)
	}
}
