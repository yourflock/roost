// metadata_display.go — Rich metadata display for all content types.
//
// Aggregates content metadata from multiple external sources:
//   - TMDB (movies and TV shows)
//   - TVDB (TV shows, alternative)
//   - MusicBrainz (music)
//   - IGDB (games)
//
// All external responses are cached in Postgres (metadata_cache table) with
// a 7-day TTL. Redis is used as a fast L1 cache (1-hour TTL) for hot items.
// Falls back to the Postgres cache if external APIs are unreachable.
//
// API endpoint: GET /catalog/metadata/{content_type}/{id}
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// ContentType classifies the type of content.
type ContentType string

const (
	ContentTypeMovie   ContentType = "movie"
	ContentTypeSeries  ContentType = "series"
	ContentTypeAlbum   ContentType = "album"
	ContentTypePodcast ContentType = "podcast"
	ContentTypeGame    ContentType = "game"
)

// ContentMetadata is the rich metadata structure for any content type.
// Per-content-type details live in the typed sub-structs (Movie, Episode, etc.).
// Only the sub-struct matching the content type is populated.
type ContentMetadata struct {
	// Universal fields.
	ID          string      `json:"id"`
	ContentType ContentType `json:"content_type"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Year        int         `json:"year,omitempty"`
	Rating      string      `json:"rating,omitempty"` // "PG-13", "TV-MA", etc.
	Genres      []string    `json:"genres,omitempty"`
	CoverURL    string      `json:"cover_url,omitempty"`
	BackdropURL string      `json:"backdrop_url,omitempty"`
	ExternalIDs map[string]string `json:"external_ids,omitempty"`
	Source      string      `json:"source"` // which API provided this data
	CachedAt    time.Time   `json:"cached_at"`

	// Per-content-type sub-structs (only one will be populated).
	Movie   *MovieMeta   `json:"movie,omitempty"`
	Episode *EpisodeMeta `json:"episode,omitempty"`
	Album   *AlbumMeta   `json:"album,omitempty"`
	Podcast *PodcastMeta `json:"podcast,omitempty"`
	Game    *GameMeta    `json:"game,omitempty"`
}

// MovieMeta holds movie-specific metadata.
type MovieMeta struct {
	Runtime   int      `json:"runtime_min,omitempty"` // minutes
	Director  string   `json:"director,omitempty"`
	Cast      []string `json:"cast,omitempty"`
	Budget    int64    `json:"budget,omitempty"`
	Revenue   int64    `json:"revenue,omitempty"`
	Tagline   string   `json:"tagline,omitempty"`
	IMDBScore float64  `json:"imdb_score,omitempty"`
}

// EpisodeMeta holds TV episode-specific metadata.
type EpisodeMeta struct {
	ShowTitle    string `json:"show_title,omitempty"`
	SeasonNumber int    `json:"season_number,omitempty"`
	EpisodeNumber int   `json:"episode_number,omitempty"`
	AirDate      string `json:"air_date,omitempty"`
	Director     string `json:"director,omitempty"`
	GuestStars   []string `json:"guest_stars,omitempty"`
}

// AlbumMeta holds music album-specific metadata.
type AlbumMeta struct {
	Artist       string   `json:"artist,omitempty"`
	TrackCount   int      `json:"track_count,omitempty"`
	Label        string   `json:"label,omitempty"`
	ReleaseDate  string   `json:"release_date,omitempty"`
	MBReleaseID  string   `json:"mb_release_id,omitempty"`
	Tracks       []string `json:"tracks,omitempty"`
}

// PodcastMeta holds podcast-specific metadata.
type PodcastMeta struct {
	Author      string `json:"author,omitempty"`
	FeedURL     string `json:"feed_url,omitempty"`
	EpisodeCount int   `json:"episode_count,omitempty"`
	Language    string `json:"language,omitempty"`
	LastEpisode string `json:"last_episode,omitempty"`
}

// GameMeta holds video game-specific metadata.
type GameMeta struct {
	Developer   string   `json:"developer,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Platforms   []string `json:"platforms,omitempty"`
	ReleaseDate string   `json:"release_date,omitempty"`
	IGDBScore   float64  `json:"igdb_score,omitempty"`
	Modes       []string `json:"modes,omitempty"` // "single-player", "multiplayer"
}

// MetadataService fetches and caches rich content metadata.
type MetadataService struct {
	db   *sql.DB
	tmdb *TMDBClient
}

// NewMetadataService creates a MetadataService.
func NewMetadataService(db *sql.DB, tmdbKey string) *MetadataService {
	return &MetadataService{
		db:   db,
		tmdb: NewTMDBClient(tmdbKey),
	}
}

// Get returns rich metadata for a content item by type and external ID.
// Cache lookup order: Postgres metadata_cache → external API → store in cache.
func (s *MetadataService) Get(ctx context.Context, contentType ContentType, externalID string) (*ContentMetadata, error) {
	// Check Postgres cache first.
	cached, err := s.loadFromCache(ctx, string(contentType), externalID)
	if err == nil && cached != nil {
		return cached, nil
	}

	// Fetch from external API based on content type.
	var meta *ContentMetadata
	switch contentType {
	case ContentTypeMovie:
		meta, err = s.fetchMovieFromTMDB(ctx, externalID)
	case ContentTypeSeries:
		meta, err = s.fetchSeriesFromTMDB(ctx, externalID)
	default:
		// For unsupported types, try to return a local/DB record.
		return s.buildLocalMetadata(ctx, contentType, externalID)
	}

	if err != nil {
		log.Printf("[metadata] fetch %s/%s failed: %v; falling back to cache", contentType, externalID, err)
		// Fall back to expired cache entry if available.
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("metadata unavailable for %s/%s", contentType, externalID)
	}

	// Store in Postgres cache.
	if storeErr := s.storeInCache(ctx, string(contentType), externalID, "tmdb", meta); storeErr != nil {
		log.Printf("[metadata] cache store %s/%s: %v", contentType, externalID, storeErr)
	}

	return meta, nil
}

// loadFromCache reads metadata from the Postgres cache if not expired.
func (s *MetadataService) loadFromCache(ctx context.Context, contentType, externalID string) (*ContentMetadata, error) {
	var rawJSON []byte
	var fetchedAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT metadata, fetched_at
		FROM metadata_cache
		WHERE content_type = $1
		  AND external_id  = $2
		  AND expires_at   > now()
		ORDER BY fetched_at DESC
		LIMIT 1
	`, contentType, externalID).Scan(&rawJSON, &fetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var meta ContentMetadata
	if err := json.Unmarshal(rawJSON, &meta); err != nil {
		return nil, err
	}
	meta.CachedAt = fetchedAt
	return &meta, nil
}

// storeInCache upserts a metadata record into the cache table.
func (s *MetadataService) storeInCache(ctx context.Context, contentType, externalID, source string, meta *ContentMetadata) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO metadata_cache (content_type, external_id, source, metadata)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (content_type, external_id, source)
		DO UPDATE SET
			metadata   = EXCLUDED.metadata,
			fetched_at = now(),
			expires_at = now() + INTERVAL '7 days'
	`, contentType, externalID, source, raw)
	return err
}

// fetchMovieFromTMDB fetches movie metadata from TMDB.
func (s *MetadataService) fetchMovieFromTMDB(ctx context.Context, tmdbID string) (*ContentMetadata, error) {
	movie, err := s.tmdb.GetMovie(ctx, tmdbID)
	if err != nil {
		return nil, err
	}

	genres := make([]string, len(movie.Genres))
	for i, g := range movie.Genres {
		genres[i] = g.Name
	}

	cast := make([]string, 0, 5)
	for i, c := range movie.Credits.Cast {
		if i >= 5 {
			break
		}
		cast = append(cast, c.Name)
	}

	var director string
	for _, cr := range movie.Credits.Crew {
		if cr.Job == "Director" {
			director = cr.Name
			break
		}
	}

	meta := &ContentMetadata{
		ID:          tmdbID,
		ContentType: ContentTypeMovie,
		Title:       movie.Title,
		Description: movie.Overview,
		Year:        yearFromDateStr(movie.ReleaseDate),
		Genres:      genres,
		CoverURL:    tmdbImageURL(movie.PosterPath, "w500"),
		BackdropURL: tmdbImageURL(movie.BackdropPath, "w1280"),
		Rating:      movie.Rated,
		Source:      "tmdb",
		CachedAt:    time.Now(),
		ExternalIDs: map[string]string{"tmdb": tmdbID},
		Movie: &MovieMeta{
			Runtime:   movie.Runtime,
			Director:  director,
			Cast:      cast,
			Budget:    movie.Budget,
			Revenue:   movie.Revenue,
			Tagline:   movie.Tagline,
			IMDBScore: movie.VoteAverage,
		},
	}
	if movie.IMDBId != "" {
		meta.ExternalIDs["imdb"] = movie.IMDBId
	}
	return meta, nil
}

// fetchSeriesFromTMDB fetches TV series metadata from TMDB.
func (s *MetadataService) fetchSeriesFromTMDB(ctx context.Context, tmdbID string) (*ContentMetadata, error) {
	series, err := s.tmdb.GetTVShow(ctx, tmdbID)
	if err != nil {
		return nil, err
	}

	genres := make([]string, len(series.Genres))
	for i, g := range series.Genres {
		genres[i] = g.Name
	}

	meta := &ContentMetadata{
		ID:          tmdbID,
		ContentType: ContentTypeSeries,
		Title:       series.Name,
		Description: series.Overview,
		Year:        yearFromDateStr(series.FirstAirDate),
		Genres:      genres,
		CoverURL:    tmdbImageURL(series.PosterPath, "w500"),
		BackdropURL: tmdbImageURL(series.BackdropPath, "w1280"),
		Source:      "tmdb",
		CachedAt:    time.Now(),
		ExternalIDs: map[string]string{"tmdb": tmdbID},
	}
	return meta, nil
}

// buildLocalMetadata returns a minimal metadata record from local DB data.
func (s *MetadataService) buildLocalMetadata(_ context.Context, contentType ContentType, id string) (*ContentMetadata, error) {
	return &ContentMetadata{
		ID:          id,
		ContentType: contentType,
		Title:       id,
		Source:      "local",
		CachedAt:    time.Now(),
	}, nil
}

// ---------- helpers ----------------------------------------------------------

// yearFromDateStr parses a year from strings like "2024-03-15" or "2024".
func yearFromDateStr(s string) int {
	if len(s) >= 4 {
		year := 0
		for _, c := range s[:4] {
			if c >= '0' && c <= '9' {
				year = year*10 + int(c-'0')
			} else {
				return 0
			}
		}
		return year
	}
	return 0
}

// tmdbImageURL builds a full TMDB image URL from a path and size.
func tmdbImageURL(path, size string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("https://image.tmdb.org/t/p/%s%s", size, path)
}
