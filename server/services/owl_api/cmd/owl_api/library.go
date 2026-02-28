// library.go — Unified content library endpoint for the Roost Owl Addon API.
//
// The /owl/v1/library endpoint returns a paginated catalog of all non-live content
// types hosted by Roost: movies, series, music albums, podcasts, and games.
// Owl clients use this to populate their "Library" tab for the Roost addon source.
//
// Endpoint:
//
//	GET /owl/v1/library
//	    ?type=movie|series|music|podcast|game  — filter by type (default: all)
//	    ?q=search_query                         — full-text search across title/name
//	    ?limit=50                               — page size (default 50, max 200)
//	    ?offset=0                               — pagination offset
//
// Response:
//
//	{
//	  "items": [ LibraryItem... ],
//	  "total":  <count for current page>,
//	  "limit":  50,
//	  "offset": 0,
//	  "type_counts": { "movie": 200, "series": 80, ... }
//	}
//
// LibraryItem fields:
//
//	id, type, title, description, cover_url, year, genres[],
//	stream_url (signed, 15-min expiry; empty for multi-episode content),
//	stream_expiry, metadata (type-specific extras)
//
// Security:
//   - requireSession middleware validates the session token before this handler runs.
//   - Source URLs (m3u8, direct file, R2 keys) are NEVER returned.
//   - Stream URLs are HMAC-signed with CF_STREAM_SIGNING_KEY, 15-min TTL.
//   - Tables that don't exist yet (music_albums, podcasts, games) degrade gracefully.
package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LibraryItem is a single content item returned by GET /owl/v1/library.
// All content types share this envelope; type-specific fields are in Metadata.
type LibraryItem struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`  // movie | series | music | podcast | game
	Title        string                 `json:"title"`
	Description  string                 `json:"description,omitempty"`
	CoverURL     string                 `json:"cover_url,omitempty"`
	Year         int                    `json:"year,omitempty"`
	Genres       []string               `json:"genres,omitempty"`
	StreamURL    string                 `json:"stream_url,omitempty"`   // signed relay URL (leaf items only)
	StreamExpiry string                 `json:"stream_expiry,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// handleLibrary handles GET /owl/v1/library.
// Returns a unified paginated catalog of all Roost content types.
func (s *server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// ── Parse query params ────────────────────────────────────────────────────

	typeFilter := strings.ToLower(r.URL.Query().Get("type"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	// Validate type filter.
	validTypes := map[string]bool{
		"movie": true, "series": true, "music": true, "podcast": true, "game": true,
	}
	if typeFilter != "" && !validTypes[typeFilter] {
		writeError(w, http.StatusBadRequest, "invalid_type",
			"type must be one of: movie, series, music, podcast, game")
		return
	}

	// ── Determine which types to query ───────────────────────────────────────

	types := []string{"movie", "series", "music", "podcast", "game"}
	if typeFilter != "" {
		types = []string{typeFilter}
	}

	// When querying all types, split the limit budget across types so we get
	// a representative mix. Each type gets limit/5 + 1 rows, then we trim.
	perTypeBudget := limit
	if typeFilter == "" {
		perTypeBudget = limit/len(types) + 1
	}

	// ── Fetch items ───────────────────────────────────────────────────────────

	var items []LibraryItem
	for _, t := range types {
		fetched, err := s.fetchLibraryItems(r, t, query, perTypeBudget, offset)
		if err != nil {
			// Log but continue — partial results are better than a full failure.
			fmt.Printf("[library] fetch %s error: %v\n", t, err)
			continue
		}
		items = append(items, fetched...)
	}

	// Trim to the requested limit after cross-type aggregation.
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []LibraryItem{}
	}

	// ── Type counts (Owl sidebar badges) ─────────────────────────────────────
	typeCounts := s.fetchTypeCounts(r)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":       items,
		"total":       len(items),
		"limit":       limit,
		"offset":      offset,
		"type_counts": typeCounts,
		"updated_at":  time.Now().UTC().Format(time.RFC3339),
	})
}

// fetchLibraryItems dispatches to the appropriate type-specific fetch function.
func (s *server) fetchLibraryItems(r *http.Request, contentType, query string, limit, offset int) ([]LibraryItem, error) {
	switch contentType {
	case "movie":
		return s.libFetchMovies(r, query, limit, offset)
	case "series":
		return s.libFetchSeries(r, query, limit, offset)
	case "music":
		return s.libFetchMusic(r, query, limit, offset)
	case "podcast":
		return s.libFetchPodcasts(r, query, limit, offset)
	case "game":
		return s.libFetchGames(r, query, limit, offset)
	default:
		return nil, nil
	}
}

// ── Movies ────────────────────────────────────────────────────────────────────

func (s *server) libFetchMovies(r *http.Request, query string, limit, offset int) ([]LibraryItem, error) {
	args := []interface{}{}
	conds := []string{"is_active = true", "content_type = 'movie'"}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conds = append(conds, fmt.Sprintf("LOWER(title) LIKE $%d", len(args)))
	}
	args = append(args, limit, offset)
	lIdx, oIdx := len(args)-1, len(args)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title,
		       COALESCE(description, ''), COALESCE(cover_url, ''),
		       COALESCE(release_year, 0), COALESCE(genres, ''),
		       COALESCE(duration_seconds, 0), COALESCE(r2_hls_key, ''),
		       COALESCE(tmdb_score, 0)
		FROM catalog_items
		WHERE %s
		ORDER BY title ASC
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), lIdx, oIdx), args...)
	if err != nil {
		if libTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		var id, title, desc, cover, genresStr, hlsKey string
		var year, duration int
		var score float64
		if err := rows.Scan(&id, &title, &desc, &cover, &year, &genresStr, &duration, &hlsKey, &score); err != nil {
			continue
		}
		su, se := libSignedStreamURL(s, id, hlsKey)
		items = append(items, LibraryItem{
			ID: id, Type: "movie", Title: title,
			Description: desc, CoverURL: cover, Year: year,
			Genres:       libSplitGenres(genresStr),
			StreamURL:    su, StreamExpiry: se,
			Metadata: map[string]interface{}{
				"duration_seconds": duration,
				"tmdb_score":       score,
			},
		})
	}
	return items, rows.Err()
}

// ── Series ────────────────────────────────────────────────────────────────────

func (s *server) libFetchSeries(r *http.Request, query string, limit, offset int) ([]LibraryItem, error) {
	args := []interface{}{}
	conds := []string{"is_active = true", "content_type = 'series'"}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conds = append(conds, fmt.Sprintf("LOWER(title) LIKE $%d", len(args)))
	}
	args = append(args, limit, offset)
	lIdx, oIdx := len(args)-1, len(args)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title,
		       COALESCE(description, ''), COALESCE(cover_url, ''),
		       COALESCE(release_year, 0), COALESCE(genres, ''),
		       COALESCE(episode_count, 0), COALESCE(season_count, 0)
		FROM catalog_items
		WHERE %s
		ORDER BY title ASC
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), lIdx, oIdx), args...)
	if err != nil {
		if libTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.unity.dev")
	var items []LibraryItem
	for rows.Next() {
		var id, title, desc, cover, genresStr string
		var year, episodes, seasons int
		if err := rows.Scan(&id, &title, &desc, &cover, &year, &genresStr, &episodes, &seasons); err != nil {
			continue
		}
		items = append(items, LibraryItem{
			ID: id, Type: "series", Title: title,
			Description: desc, CoverURL: cover, Year: year,
			Genres: libSplitGenres(genresStr),
			// Series has no single stream_url — Owl fetches episodes via /owl/v1/vod/{id}.
			StreamURL: fmt.Sprintf("%s/owl/v1/vod/%s", baseURL, id),
			Metadata: map[string]interface{}{
				"episode_count": episodes,
				"season_count":  seasons,
				"has_episodes":  episodes > 0,
			},
		})
	}
	return items, rows.Err()
}

// ── Music albums ──────────────────────────────────────────────────────────────

func (s *server) libFetchMusic(r *http.Request, query string, limit, offset int) ([]LibraryItem, error) {
	args := []interface{}{}
	conds := []string{"is_active = true"}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conds = append(conds, fmt.Sprintf(
			"(LOWER(album_title) LIKE $%d OR LOWER(artist) LIKE $%d)", len(args), len(args)))
	}
	args = append(args, limit, offset)
	lIdx, oIdx := len(args)-1, len(args)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, artist, album_title,
		       COALESCE(cover_url, ''), COALESCE(release_year, 0),
		       COALESCE(genre, ''), COALESCE(track_count, 0), COALESCE(mb_release_id, '')
		FROM music_albums
		WHERE %s
		ORDER BY artist ASC, album_title ASC
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), lIdx, oIdx), args...)
	if err != nil {
		if libTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		var id, artist, album, cover, genre, mbID string
		var year, trackCount int
		if err := rows.Scan(&id, &artist, &album, &cover, &year, &genre, &trackCount, &mbID); err != nil {
			continue
		}
		items = append(items, LibraryItem{
			ID: id, Type: "music", Title: album,
			CoverURL: cover, Year: year,
			Genres: libSplitGenres(genre),
			Metadata: map[string]interface{}{
				"artist":      artist,
				"track_count": trackCount,
				"mb_id":       mbID,
			},
		})
	}
	return items, rows.Err()
}

// ── Podcasts ──────────────────────────────────────────────────────────────────

func (s *server) libFetchPodcasts(r *http.Request, query string, limit, offset int) ([]LibraryItem, error) {
	args := []interface{}{}
	conds := []string{"is_active = true"}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conds = append(conds, fmt.Sprintf("LOWER(title) LIKE $%d", len(args)))
	}
	args = append(args, limit, offset)
	lIdx, oIdx := len(args)-1, len(args)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title, COALESCE(description, ''),
		       COALESCE(cover_url, ''), COALESCE(language, 'en'), COALESCE(episode_count, 0)
		FROM podcasts
		WHERE %s
		ORDER BY title ASC
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), lIdx, oIdx), args...)
	if err != nil {
		if libTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		var id, title, desc, cover, lang string
		var epCount int
		if err := rows.Scan(&id, &title, &desc, &cover, &lang, &epCount); err != nil {
			continue
		}
		items = append(items, LibraryItem{
			ID: id, Type: "podcast", Title: title,
			Description: desc, CoverURL: cover,
			Metadata: map[string]interface{}{
				"language":      lang,
				"episode_count": epCount,
				// rss_url intentionally omitted — fetch episodes via /owl/v1/vod/{id}
			},
		})
	}
	return items, rows.Err()
}

// ── Games ─────────────────────────────────────────────────────────────────────

func (s *server) libFetchGames(r *http.Request, query string, limit, offset int) ([]LibraryItem, error) {
	args := []interface{}{}
	conds := []string{"is_active = true"}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conds = append(conds, fmt.Sprintf("LOWER(title) LIKE $%d", len(args)))
	}
	args = append(args, limit, offset)
	lIdx, oIdx := len(args)-1, len(args)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title, COALESCE(cover_url, ''), COALESCE(release_year, 0),
		       COALESCE(genre, ''), platform, players, save_slots,
		       COALESCE(igdb_score, 0), COALESCE(summary, '')
		FROM games
		WHERE %s
		ORDER BY title ASC
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), lIdx, oIdx), args...)
	if err != nil {
		if libTableMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		var id, title, cover, genre, platform, summary string
		var year, players, saveSlots int
		var igdbScore float64
		if err := rows.Scan(&id, &title, &cover, &year, &genre, &platform,
			&players, &saveSlots, &igdbScore, &summary); err != nil {
			continue
		}
		items = append(items, LibraryItem{
			ID: id, Type: "game", Title: title,
			Description: summary, CoverURL: cover, Year: year,
			Genres: libSplitGenres(genre),
			Metadata: map[string]interface{}{
				"platform":   platform,
				"players":    players,
				"save_slots": saveSlots,
				"igdb_score": igdbScore,
			},
		})
	}
	return items, rows.Err()
}

// ── Type counts ───────────────────────────────────────────────────────────────

// fetchTypeCounts returns the active item count for each content type.
// Used by Owl to display badge counts in the library sidebar.
// Tables that don't exist yet return 0 gracefully.
func (s *server) fetchTypeCounts(r *http.Request) map[string]int {
	counts := map[string]int{
		"movie":   0,
		"series":  0,
		"music":   0,
		"podcast": 0,
		"game":    0,
	}

	// Movies + series from catalog_items.
	catRows, err := s.db.QueryContext(r.Context(), `
		SELECT content_type, COUNT(*) FROM catalog_items
		WHERE is_active = true AND content_type IN ('movie', 'series')
		GROUP BY content_type
	`)
	if err == nil {
		defer catRows.Close()
		for catRows.Next() {
			var t string
			var n int
			if catRows.Scan(&t, &n) == nil {
				counts[t] = n
			}
		}
	}

	// Music albums.
	var n int
	if s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM music_albums WHERE is_active = true`).Scan(&n) == nil {
		counts["music"] = n
	}

	// Podcasts.
	n = 0
	if s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcasts WHERE is_active = true`).Scan(&n) == nil {
		counts["podcast"] = n
	}

	// Games.
	n = 0
	if s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM games WHERE is_active = true`).Scan(&n) == nil {
		counts["game"] = n
	}

	return counts
}

// ── Package-local helpers ─────────────────────────────────────────────────────

// libSplitGenres splits a comma-separated genre string into a slice.
// Returns nil (omitted in JSON) for empty input.
func libSplitGenres(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// libTableMissing returns true when the DB error indicates a missing relation.
// Allows graceful degradation when newer content tables haven't been migrated yet.
func libTableMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "42P01") // PostgreSQL SQLSTATE: undefined_table
}

// libSignedStreamURL generates a signed relay URL for a catalog item.
// Returns ("", "") when the item has no HLS key (series, podcasts — browse only).
func libSignedStreamURL(s *server, id, hlsKey string) (string, string) {
	if hlsKey == "" {
		return "", ""
	}
	url, expiresAt := signedStreamURL(id)
	return url, expiresAt.Format(time.RFC3339)
}
